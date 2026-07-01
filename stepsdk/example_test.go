package stepsdk_test

import (
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/warden/stepsdk"
)

// A complete custom step: block a push that touches too many files at once.
// The whole program is a handler plus a one-line main.
func Example() {
	handler := func(in stepsdk.Input) stepsdk.Output {
		if in.DiffSummary.FilesTouched > 5 {
			return stepsdk.Fail(stepsdk.Finding{
				Severity: "medium",
				Message:  fmt.Sprintf("%d files in one push; split it up", in.DiffSummary.FilesTouched),
			})
		}
		return stepsdk.Pass()
	}

	// In a real step this is simply: func main() { stepsdk.Run(handler) }
	// Here we drive the same core with an explicit reader/writer to show output.
	in := `{"schema_version":1,"diff_summary":{"files_touched":6,"lines_changed":210}}`
	_ = stepsdk.RunWith(strings.NewReader(in), os.Stdout, handler)
	// Output: {"schema_version":1,"status":"fail","findings":[{"severity":"medium","message":"6 files in one push; split it up"}],"fixed":false}
}
