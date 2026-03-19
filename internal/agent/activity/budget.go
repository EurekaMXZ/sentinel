package activity

import (
	"encoding/json"

	"sentinel/internal/agent"
)

// bytesPerEstimatedToken is deliberately conservative. Exact usage from the
// previous response anchors the estimate; only newly appended items are
// approximated, so tokenizer differences do not accumulate.
const bytesPerEstimatedToken = 3

type budgetAssessment struct {
	NeedsCompact  bool
	ContextTokens int64
	RequestBytes  int
}

func estimateTokens(size int) int64 {
	if size <= 0 {
		return 0
	}
	return int64((size + bytesPerEstimatedToken - 1) / bytesPerEstimatedToken)
}

// assessBudget estimates the next model request. inputTokens and appendedBytes
// describe the delta since the request whose usage was reported; pass
// inputTokens=0 to estimate the serialized request directly.
func (a *Activities) assessBudget(task agent.Task, items []json.RawMessage, appendedBytes int, inputTokens int64) (budgetAssessment, error) {
	request := a.modelRequest(task, rawInputItems(items))
	data, err := json.Marshal(request)
	if err != nil {
		return budgetAssessment{}, permanent("invalid_context_estimate")
	}
	tokens := estimateTokens(len(data))
	if inputTokens > 0 {
		tokens = inputTokens + estimateTokens(appendedBytes)
	}
	assessment := budgetAssessment{ContextTokens: tokens, RequestBytes: len(data)}
	assessment.NeedsCompact = tokens+task.Limits.MaxOutputTokens+task.Limits.ContextSafetyTokens > task.Limits.ContextWindowTokens ||
		len(data) > task.Limits.MaxContextBytes/2
	return assessment, nil
}
