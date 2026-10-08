package train

import (
	"fmt"
	"net/url"
	"regexp"
)

// webURL is the GitHub web host the PR links point at.
const webURL = "https://github.com"

// PRURL returns the web URL of pull request n of owner/repo.
func PRURL(owner, repo string, n int) string {
	return fmt.Sprintf("%s/%s/%s/pull/%d", webURL, url.PathEscape(owner), url.PathEscape(repo), n)
}

// PRLink returns a Markdown link to pull request n of owner/repo, labelled "repo#n". The
// tracking issue lives in the workflows repo, where GitHub links a bare "#n" to the workflows
// repo's own issue or PR n, so every PR reference names its repo. Without an owner it is the
// label alone.
func PRLink(owner, repo string, n int) string {
	label := fmt.Sprintf("%s#%d", repo, n)
	if owner == "" {
		return label
	}
	return fmt.Sprintf("[%s](%s)", label, PRURL(owner, repo, n))
}

// prLink is PRLink for a repo of the train.
func (e *Engine) prLink(repo string, n int) string { return PRLink(e.Owner, repo, n) }

// mdLink matches the Markdown links PRLink writes.
var mdLink = regexp.MustCompile(`\[([^\[\]]+)\]\((https://[^()\s]+)\)`)

// SlackText turns the Markdown links in s into Slack mrkdwn links (<url|label>), so the same
// detail reads well in the tracking issue and in the train's Slack thread.
func SlackText(s string) string {
	return mdLink.ReplaceAllString(s, "<$2|$1>")
}
