package api

import (
	"context"
	"net/http"
	"testing"

	"sentinel/internal/control"
	"sentinel/internal/source"
)

type fakeControlStore struct {
	projects map[string]control.Project
	analyses map[string]control.Analysis
}

func newFakeControlStore() *fakeControlStore {
	return &fakeControlStore{projects: make(map[string]control.Project), analyses: make(map[string]control.Analysis)}
}

func (f *fakeControlStore) CreateProject(_ context.Context, project control.Project) (control.Project, error) {
	f.projects[project.ID] = project
	return project, nil
}

func (f *fakeControlStore) ListProjects(_ context.Context, _ string, _ int) ([]control.Project, error) {
	result := make([]control.Project, 0, len(f.projects))
	for _, project := range f.projects {
		result = append(result, project)
	}
	return result, nil
}

func (f *fakeControlStore) LoadProject(_ context.Context, _, projectID string) (control.Project, error) {
	project, ok := f.projects[projectID]
	if !ok {
		return control.Project{}, control.ErrNotFound
	}
	return project, nil
}

func (f *fakeControlStore) UpdateProject(_ context.Context, project control.Project) (control.Project, error) {
	if _, ok := f.projects[project.ID]; !ok {
		return control.Project{}, control.ErrNotFound
	}
	f.projects[project.ID] = project
	return project, nil
}

func (f *fakeControlStore) AddProjectRepository(_ context.Context, _, projectID, repositoryID string, primary bool) (control.ProjectRepository, error) {
	project, ok := f.projects[projectID]
	if !ok {
		return control.ProjectRepository{}, control.ErrNotFound
	}
	binding := control.ProjectRepository{RepositoryID: repositoryID, RemoteURL: "https://github.com/owner/repository.git",
		RepositoryStatus: "active", TrackedRefs: []string{"refs/heads/*"}, ObjectFormat: "sha1", IsPrimary: primary}
	project.Repositories = append(project.Repositories, binding)
	f.projects[projectID] = project
	return binding, nil
}

func (f *fakeControlStore) RemoveProjectRepository(_ context.Context, _, projectID, repositoryID string) error {
	project, ok := f.projects[projectID]
	if !ok {
		return control.ErrNotFound
	}
	filtered := project.Repositories[:0]
	for _, binding := range project.Repositories {
		if binding.RepositoryID != repositoryID {
			filtered = append(filtered, binding)
		}
	}
	project.Repositories = filtered
	f.projects[projectID] = project
	return nil
}

func (f *fakeControlStore) ListProjectRepositories(_ context.Context, _, projectID string) ([]control.ProjectRepository, error) {
	project, ok := f.projects[projectID]
	if !ok {
		return nil, control.ErrNotFound
	}
	return project.Repositories, nil
}

func (f *fakeControlStore) CreateAnalysis(_ context.Context, analysis control.Analysis) (control.Analysis, error) {
	f.analyses[analysis.ID] = analysis
	return analysis, nil
}

func (f *fakeControlStore) ListProjectAnalyses(_ context.Context, _, projectID string, _ int) ([]control.Analysis, error) {
	result := make([]control.Analysis, 0)
	for _, analysis := range f.analyses {
		if analysis.ProjectID == projectID {
			result = append(result, analysis)
		}
	}
	return result, nil
}

func (f *fakeControlStore) LoadAnalysis(_ context.Context, _, analysisID string) (control.Analysis, error) {
	analysis, ok := f.analyses[analysisID]
	if !ok {
		return control.Analysis{}, control.ErrNotFound
	}
	return analysis, nil
}

type fakeSourceAdmin struct {
	snapshot source.Snapshot
}

func (f *fakeSourceAdmin) UpdateRepository(_ context.Context, repository source.Repository) (source.Repository, error) {
	return repository, nil
}

func (f *fakeSourceAdmin) LoadSnapshot(_ context.Context, _, repositoryID, snapshotID string) (source.Snapshot, error) {
	if f.snapshot.ID != snapshotID || f.snapshot.RepositoryID != repositoryID {
		return source.Snapshot{}, source.ErrNotFound
	}
	return f.snapshot, nil
}

func (f *fakeSourceAdmin) ListRefs(context.Context, string, string, int) ([]source.RefRecord, error) {
	return nil, nil
}

func (f *fakeSourceAdmin) ListSyncs(context.Context, string, string, int) ([]source.SyncRecord, error) {
	return nil, nil
}

func TestProjectAnalysisEndpoints(t *testing.T) {
	deps, _, creator, _, _, _, _ := controlDependencies()
	store := newFakeControlStore()
	deps.Control = store
	deps.SourceAdmin = &fakeSourceAdmin{snapshot: source.Snapshot{
		ID:           "0194e229-8576-7f42-8f08-3496913847cb",
		RepositoryID: testRepo,
		CommitSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TreeSHA:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Status:       source.SnapshotReady,
	}}
	router := testRouter(deps)

	created := requestJSON(t, router, http.MethodPost, "/v1/projects", testTenant, map[string]any{
		"name": "Demo project", "description": "control plane test",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", created.Code, created.Body.String())
	}
	projectID, _ := decode(t, created)["project_id"].(string)
	if projectID == "" {
		t.Fatalf("missing project ID: %s", created.Body.String())
	}

	bound := requestJSON(t, router, http.MethodPost, "/v1/projects/"+projectID+"/repositories", testTenant, map[string]any{
		"repository_id": testRepo, "is_primary": true,
	})
	if bound.Code != http.StatusCreated {
		t.Fatalf("bind repository status=%d body=%s", bound.Code, bound.Body.String())
	}

	analysis := requestJSON(t, router, http.MethodPost, "/v1/projects/"+projectID+"/analyses", testTenant, map[string]any{
		"repository_id": testRepo,
		"snapshot_id":   "0194e229-8576-7f42-8f08-3496913847cb",
		"task":          "review this snapshot",
	})
	if analysis.Code != http.StatusAccepted {
		t.Fatalf("create analysis status=%d body=%s", analysis.Code, analysis.Body.String())
	}
	body := decode(t, analysis)
	if body["agent_id"] != testAgent || body["project_id"] != projectID {
		t.Fatalf("unexpected analysis response: %s", analysis.Body.String())
	}
	if creator.calls != 1 || len(creator.resources) != 1 ||
		creator.resources[0].SourceSnapshotID != "0194e229-8576-7f42-8f08-3496913847cb" {
		t.Fatalf("unexpected Agent creation: calls=%d resources=%+v", creator.calls, creator.resources)
	}

	listed := requestJSON(t, router, http.MethodGet, "/v1/projects/"+projectID+"/analyses", testTenant, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list analyses status=%d body=%s", listed.Code, listed.Body.String())
	}
	analysisID, _ := body["analysis_id"].(string)
	loaded := requestJSON(t, router, http.MethodGet, "/v1/analyses/"+analysisID, testTenant, nil)
	if loaded.Code != http.StatusOK || decode(t, loaded)["analysis_id"] != analysisID {
		t.Fatalf("load analysis status=%d body=%s", loaded.Code, loaded.Body.String())
	}
}
