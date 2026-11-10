package workflow

import (
	"path/filepath"
	"testing"

	"go.temporal.io/sdk/worker"
)

// Histories recorded on the live stack before the engineering steps existed
// (a completed and a cancelled run) must replay unchanged: in-flight
// workflows keep working across upgrades.
func TestRecordedHistoriesReplay(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "history-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no recorded histories: %v", err)
	}
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(PipelineWorkflow)
	for _, f := range files {
		if err := r.ReplayWorkflowHistoryFromJSONFile(nil, f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}
