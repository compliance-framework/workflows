package train

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// commands applies the comment commands posted since the last run, oldest first.
func (e *Engine) commands(ctx context.Context, is Issue, st *State) error {
	comments, err := e.Tracker.Comments(ctx, is.Number, st.LastComment)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range comments {
		st.LastComment = max(st.LastComment, c.ID)
		cmd, ok, perr := ParseCommand(c.Body)
		if !ok {
			continue
		}
		reply := perr
		if reply == nil {
			reply = e.apply(ctx, st, c.Author, cmd)
		}
		msg := "done"
		if reply != nil {
			msg = reply.Error()
		}
		e.logf("command from %s: %q: %s", c.Author, strings.TrimSpace(c.Body), msg)
		if _, err := e.Tracker.Comment(ctx, is.Number, fmt.Sprintf("@%s `%s`: %s.", c.Author, strings.Join(strings.Fields(cmdLine(c.Body)), " "), msg)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cmdLine(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	return strings.ReplaceAll(line, "`", "")
}

// apply runs one command from author; the error is the reply.
func (e *Engine) apply(ctx context.Context, st *State, author string, c Command) error {
	if st.Status != StatusOpen {
		return errors.New("the train is " + st.Status)
	}
	if e.Members == nil {
		return errors.New("refused: the train can't check org roles in this run")
	}
	admin, err := e.Members.IsOrgAdmin(ctx, author)
	if err != nil {
		return fmt.Errorf("refused: checking your org role failed: %v", err)
	}
	if !admin {
		return errors.New("refused: only organization owners can run train commands")
	}
	if c.Name == "abort" {
		st.Status = StatusAborted
		e.notify(ctx, st, "aborted", fmt.Sprintf(":octagonal_sign: *Release train %s* was aborted by %s.", st.Month, author))
		return nil
	}
	r := st.Repo(c.Repo)
	switch {
	case r == nil:
		return fmt.Errorf("%s is not in this train", c.Repo)
	case r.Phase == Released:
		return fmt.Errorf("%s is already released", r.Name)
	case c.Name == "skip":
		r.next(Skipped)
		r.Detail = "skipped by " + author
		e.notify(ctx, st, r.Name+"|skipped", fmt.Sprintf(":fast_forward: *%s* was skipped by %s.", r.Name, author))
		return nil
	}
	// retry
	switch r.Phase {
	case Skipped:
		r.Phase = Waiting
	case Bumping:
		r.BumpPR = 0 // run ccf-bump again
	case Publishing:
		if r.FailedRun != 0 {
			if err := e.Repos.RerunFailed(ctx, r.Name, r.FailedRun); err != nil {
				return fmt.Errorf("re-running the failed release jobs of %s: %v", r.Name, err)
			}
			r.FailedRun = 0
		}
	}
	r.hold(NoHold, false, "retried by %s", author)
	st.ForgetNotified(r.Name)
	return nil
}
