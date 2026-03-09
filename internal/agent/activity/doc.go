// Package activity is reserved for model calls, context persistence, and tool dispatch.
// Reuse persisted responses by stable operation ID before retrying external calls.
// Validate resource access and source readiness before dispatch; persist resolved
// snapshot IDs in the tool operation rather than binding the Agent to one version.
package activity
