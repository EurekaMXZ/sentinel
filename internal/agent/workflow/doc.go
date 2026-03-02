// Package workflow is reserved for the Agent loop and Child Workflows.
// Keep Workflow code deterministic: use Activities for all model, storage, and Git I/O.
// Pin the default source snapshot through retries, child execution, and Continue-As-New.
package workflow
