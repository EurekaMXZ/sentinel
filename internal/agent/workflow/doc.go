// Package workflow implements the Agent loop and Child Workflows.
// Keep Workflow code deterministic: use Activities for all model, storage, and Git I/O.
// Load optional resources by Agent ID; each child selects its own task and resources.
// Retries and Continue-As-New reuse persisted tool inputs, including any snapshot IDs.
package workflow
