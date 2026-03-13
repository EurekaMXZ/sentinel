package activity

import "testing"

func TestStrictChildArguments(t *testing.T) {
	var args childArguments
	if err := strictJSON([]byte(`{"task":"child-only-task"}`), &args); err != nil || args.Task != "child-only-task" {
		t.Fatalf("args=%+v err=%v", args, err)
	}
	for _, data := range []string{`{}`, `{"task":"child","extra":1}`, `{"task":"child"} {}`, `{`} {
		var args childArguments
		err := strictJSON([]byte(data), &args)
		if err == nil && args.Task != "" {
			t.Errorf("unexpectedly accepted arguments: %s", data)
		}
	}
}
