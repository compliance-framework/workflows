package train

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// PRURL returns the web URL of pull request n of repo. reposURL is the web URL of the repos'
// owner, e.g. https://github.com/compliance-framework.
func PRURL(reposURL, repo string, n int) string {
	return fmt.Sprintf("%s/%s/pull/%d", strings.TrimRight(reposURL, "/"), url.PathEscape(repo), n)
}

// PRLink returns a Markdown link to pull request n of repo, labelled "repo#n". The tracking
// issue lives in the workflows repo, where GitHub links a bare "#n" to the workflows repo's own
// issue or PR n, so every PR reference names its repo. Without reposURL it is the label alone.
func PRLink(reposURL, repo string, n int) string {
	label := fmt.Sprintf("%s#%d", repo, n)
	if reposURL == "" {
		return label
	}
	return fmt.Sprintf("[%s](%s)", label, PRURL(reposURL, repo, n))
}

// prLink is PRLink for a repo of the train.
func (e *Engine) prLink(repo string, n int) string { return PRLink(e.ReposURL, repo, n) }

// mdLink matches the Markdown links PRLink writes.
var mdLink = regexp.MustCompile(`\[([^\[\]]+)\]\((https?://[^()\s]+)\)`)

// SlackText turns the Markdown links in s into Slack mrkdwn links (<url|label>), so the same
// detail reads well in the tracking issue and in the train's Slack thread.
func SlackText(s string) string {
	return mdLink.ReplaceAllString(s, "<$2|$1>")
}
