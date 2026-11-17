package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/user/mini_code/job"
)

type AsyncCall struct {
	Job *job.Job
	Settled bool
	Note string
}

type AsyncTool interface {
	Name() string
	StartAsync(ctx context.Context, owner string, params map[string]interface{}, sink func(string)) (*AsyncCall, error)
}

type JobHost interface {
	Jobs() *job.Registry
}

func promoteNote(j *job.Job, waited time.Duration) string {
	body := j.Output(true)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return fmt.Sprintf("%s[still running after %dms; moved to background job %s]",
		body, waited.Milliseconds(), j.ID)
}

func backgroundNote(j *job.Job) string {
	return fmt.Sprintf("started background job %s", j.ID)
}
