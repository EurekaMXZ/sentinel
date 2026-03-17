package source

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

var (
	githubName   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	envReference = regexp.MustCompile(`^env:[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

func ValidID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil
}

// NormalizeGitHubURL rejects embedded credentials, query strings, custom ports,
// and other transports. Credentials are resolved only by a trusted Worker.
func NormalizeGitHubURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", errors.New("remote must be https://github.com/owner/repository without credentials")
	}
	parts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "" {
		return "", errors.New("remote must identify one GitHub repository")
	}
	owner, name := parts[1], strings.TrimSuffix(parts[2], ".git")
	for _, part := range []string{owner, name} {
		if !githubName.MatchString(part) || part == "." || part == ".." || len(part) > 100 {
			return "", errors.New("invalid GitHub owner or repository name")
		}
	}
	return "https://github.com/" + owner + "/" + name + ".git", nil
}

func ValidCredentialRef(ref string) bool { return ref == "" || envReference.MatchString(ref) }

func ValidateRepository(repo Repository) error {
	if !ValidID(repo.TenantID) || !ValidID(repo.ID) || !ValidCredentialRef(repo.CredentialRef) {
		return errors.New("invalid repository identity or credential reference")
	}
	canonical, err := NormalizeGitHubURL(repo.RemoteURL)
	if err != nil || canonical != repo.RemoteURL {
		return errors.New("repository must use a normalized GitHub HTTPS URL")
	}
	if repo.ObjectFormat != "sha1" && repo.ObjectFormat != "sha256" {
		return errors.New("unsupported Git object format")
	}
	if len(repo.TrackedRefs) == 0 || len(repo.TrackedRefs) > 64 {
		return errors.New("between 1 and 64 tracked refs are required")
	}
	seen := make(map[string]bool)
	for _, ref := range repo.TrackedRefs {
		if seen[ref] || !ValidTrackedRef(ref) {
			return errors.New("invalid or duplicate tracked ref")
		}
		seen[ref] = true
	}
	return nil
}

func ValidTrackedRef(ref string) bool {
	for _, ch := range ref {
		if ch < 32 || ch == 127 {
			return false
		}
	}
	if len(ref) > 512 || !(strings.HasPrefix(ref, "refs/heads/") || strings.HasPrefix(ref, "refs/tags/")) ||
		strings.ContainsAny(ref, " ~^:?[]\\\x00\r\n\t") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.Contains(ref, "//") || strings.Count(ref, "*") > 1 || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func MatchesRef(pattern, ref string) bool {
	before, after, wildcard := strings.Cut(pattern, "*")
	if !wildcard {
		return pattern == ref
	}
	return len(ref) >= len(before)+len(after) && strings.HasPrefix(ref, before) && strings.HasSuffix(ref, after)
}

func SortedRefs(refs []string) []string {
	result := slices.Clone(refs)
	slices.Sort(result)
	return result
}

func ValidOID(oid, format string) bool {
	length := 40
	if format == "sha256" {
		length = 64
	}
	if len(oid) != length || strings.Trim(oid, "0") == "" {
		return false
	}
	for _, ch := range oid {
		if !(ch >= '0' && ch <= '9') && !(ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func ValidateCandidate(c Candidate, repo Repository) error {
	if c.Version != 1 || c.Request.TenantID != repo.TenantID || c.Request.RepositoryID != repo.ID ||
		c.ExpectedArchiveVersion != repo.ArchiveVersion || c.ParentManifestID != repo.ArchiveHeadID {
		return ErrConflict
	}
	if c.NewArchive {
		m := c.Manifest
		if !ValidID(m.ID) || !ValidID(m.BundleRef) || !ValidID(c.ManifestRef) || m.RepositoryID != repo.ID ||
			m.ParentManifestID != repo.ArchiveHeadID || m.OperationID != c.Request.OperationID || c.ArchiveAttempt < 1 ||
			!ValidOID(m.BundleChecksum, "sha256") || m.CreatedAt.IsZero() ||
			(repo.ArchiveHeadID == "" && m.BundleKind != BundleBase) || (repo.ArchiveHeadID != "" && m.BundleKind != BundleIncrement) {
			return errors.New("invalid source archive candidate")
		}
		if m.BundleKind == BundleBase && len(m.PrerequisiteCommitSHAs) != 0 {
			return errors.New("base archive cannot have prerequisites")
		}
		for _, oid := range m.PrerequisiteCommitSHAs {
			if !ValidOID(oid, repo.ObjectFormat) {
				return errors.New("invalid archive prerequisite")
			}
		}
	} else if c.Manifest.ID != repo.ArchiveHeadID {
		return ErrConflict
	}
	seen := make(map[string]bool)
	for _, ref := range c.Refs {
		if !ValidTrackedRef(ref.Name) || strings.Contains(ref.Name, "*") || seen[ref.Name] || !ValidOID(ref.TargetOID, repo.ObjectFormat) ||
			(ref.CommitSHA != "" && !ValidOID(ref.CommitSHA, repo.ObjectFormat)) {
			return errors.New("invalid observed source ref")
		}
		seen[ref.Name] = true
	}
	clear(seen)
	for _, snapshot := range c.Snapshots {
		if !ValidID(snapshot.ID) || snapshot.RepositoryID != repo.ID || snapshot.StorageManifestID != c.Manifest.ID ||
			!ValidOID(snapshot.CommitSHA, repo.ObjectFormat) || !ValidOID(snapshot.TreeSHA, repo.ObjectFormat) || seen[snapshot.CommitSHA] || !ValidID(c.Manifest.ID) ||
			snapshot.Status != SnapshotReady || snapshot.CreatedAt.IsZero() {
			return errors.New("invalid source snapshot candidate")
		}
		seen[snapshot.CommitSHA] = true
	}
	return nil
}
