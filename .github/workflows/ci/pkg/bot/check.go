/*
Copyright 2021 Gravitational, Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gravitational/teleport/.github/workflows/ci"
	"github.com/gravitational/teleport/.github/workflows/ci/pkg/environment"

	"github.com/google/go-github/v37/github"
	"github.com/gravitational/trace"
)

// Check checks if all the reviewers have approved the pull request in the current context.
func (c *Bot) Check(ctx context.Context) error {
	pr := c.Environment.Metadata
	if c.Environment.IsInternal(pr.Author) {
		return c.checkInternal(ctx)
	}
	return c.checkExternal(ctx)
}

// checkInternal is called to check if a PR reviewed and approved by the
// required reviewers for internal contributors. Unlike approvals for
// external contributors, approvals from internal team members will not be
// invalidated when new changes are pushed to the PR.
func (c *Bot) checkInternal(ctx context.Context) error {
	pr := c.Environment.Metadata
	// Remove any stale workflow runs. As only the current workflow run should
	// be shown because it is the workflow that reflects the correct state of the pull request.
	//
	// Note: This is run for all workflow runs triggered by an event from an internal contributor,
	// but has to be run again in cron workflow because workflows triggered by external contributors do not
	// grant the Github actions token the correct permissions to dismiss workflow runs.
	err := c.dismissStaleWorkflowRuns(ctx, pr.RepoOwner, pr.RepoName, pr.BranchName)
	if err != nil {
		return trace.Wrap(err)
	}
	mostRecentReviews, err := c.getMostRecentReviews(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	log.Printf("Checking if %v has approvals from the required reviewers %+v", pr.Author, c.Environment.GetReviewersForAuthor(pr.Author))
	err = hasRequiredApprovals(mostRecentReviews, c.Environment.GetReviewersForAuthor(pr.Author))
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// checkExternal is called to check if a PR reviewed and approved by the
// required reviewers for external contributors. Approvals for external
// contributors are dismissed when new changes are pushed to the PR. The only
// case in which reviews are not dismissed is if they are from GitHub and
// only update the PR.
func (c *Bot) checkExternal(ctx context.Context) error {
	var obsoleteReviews map[string]review
	var validReviews map[string]review

	pr := c.Environment.Metadata
	mostRecentReviews, err := c.getMostRecentReviews(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	validReviews, obsoleteReviews = splitReviews(pr.HeadSHA, mostRecentReviews)
	// External contributions require tighter scrutiny than team
	// contributions. As such reviews from previous pushes must
	// not carry over to when new changes are added. Github does
	// not do this automatically, so we must dismiss the reviews
	// manually.
	if err = c.isGithubCommit(ctx); err != nil {
		msg := dismissMessage(pr, c.Environment.GetReviewersForAuthor(pr.Author))
		err = c.invalidateApprovals(ctx, msg, obsoleteReviews)
		if err != nil {
			return trace.Wrap(err)
		}
	}
	log.Printf("Checking if %v has approvals from the required reviewers %+v", pr.Author, c.Environment.GetReviewersForAuthor(pr.Author))
	err = hasRequiredApprovals(validReviews, c.Environment.GetReviewersForAuthor(pr.Author))
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// splitReviews splits a list of reviews into two lists: `valid` (those reviews that refer to
// the current PR head revision) and `obsolete` (those that do not)
func splitReviews(headSHA string, reviews map[string]review) (valid, obsolete map[string]review) {
	valid = make(map[string]review)
	obsolete = make(map[string]review)
	for _, r := range reviews {
		if r.commitID == headSHA {
			valid[r.name] = r
		} else {
			obsolete[r.name] = r
		}
	}
	return valid, obsolete
}

// hasRequiredApprovals determines if all required reviewers have approved.
func hasRequiredApprovals(mostRecentReviews map[string]review, required []string) error {
	if len(mostRecentReviews) == 0 {
		return trace.BadParameter("pull request has no approvals")
	}
	var waitingOnApprovalsFrom []string
	for _, requiredReviewer := range required {
		ok := hasApproved(requiredReviewer, mostRecentReviews)
		if !ok {
			waitingOnApprovalsFrom = append(waitingOnApprovalsFrom, requiredReviewer)
		}
	}
	if len(waitingOnApprovalsFrom) > 0 {
		return trace.BadParameter("required reviewers have not yet approved, waiting on approval(s) from %v", waitingOnApprovalsFrom)
	}
	return nil
}

func (c *Bot) getMostRecentReviews(ctx context.Context) (map[string]review, error) {
	env := c.Environment
	pr := c.Environment.Metadata
	reviews, _, err := env.Client.PullRequests.ListReviews(ctx, pr.RepoOwner,
		pr.RepoName,
		pr.Number,
		&github.ListOptions{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	currentReviewsSlice := []review{}
	for _, rev := range reviews {
		// Because PRs can be submitted by anyone, input here is attacker controlled
		// and do strict validation of input.
		err := validateReviewFields(rev)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		currReview := review{
			name:        *rev.User.Login,
			status:      *rev.State,
			commitID:    *rev.CommitID,
			id:          *rev.ID,
			submittedAt: rev.SubmittedAt,
		}
		currentReviewsSlice = append(currentReviewsSlice, currReview)
	}
	return mostRecent(currentReviewsSlice), nil
}

// review is a pull request review
type review struct {
	name        string
	status      string
	commitID    string
	id          int64
	submittedAt *time.Time
}

// validateReviewFields validates required fields exist and passes them
// through a restrictive allow list (alphanumerics only). This is done to
// mitigate impact of attacker controlled input (the PR).
func validateReviewFields(review *github.PullRequestReview) error {
	switch {
	case review.ID == nil:
		return trace.Errorf("review ID is nil. review: %+v", review)
	case review.State == nil:
		return trace.Errorf("review State is nil. review: %+v", review)
	case review.CommitID == nil:
		return trace.Errorf("review CommitID is nil. review: %+v", review)
	case review.SubmittedAt == nil:
		return trace.Errorf("review SubmittedAt is nil. review: %+v", review)
	case review.User.Login == nil:
		return trace.Errorf("reviewer User.Login is nil. review: %+v", review)
	}
	if err := validateField(*review.State); err != nil {
		return trace.Errorf("review ID err: %v", err)
	}
	if err := validateField(*review.CommitID); err != nil {
		return trace.Errorf("commit ID err: %v", err)
	}
	if err := validateField(*review.User.Login); err != nil {
		return trace.Errorf("user login err: %v", err)
	}
	return nil
}

// mostRecent returns a list of the most recent review from each required reviewer.
func mostRecent(currentReviews []review) map[string]review {
	mostRecentReviews := make(map[string]review)
	for _, rev := range currentReviews {
		val, ok := mostRecentReviews[rev.name]
		if !ok {
			mostRecentReviews[rev.name] = rev
		} else {
			setTime := val.submittedAt
			currTime := rev.submittedAt
			if currTime.After(*setTime) {
				mostRecentReviews[rev.name] = rev
			}
		}
	}
	return mostRecentReviews
}

// hasApproved determines if the reviewer has submitted an approval
// for the pull request.
func hasApproved(reviewer string, reviews map[string]review) bool {
	for _, rev := range reviews {
		if rev.name == reviewer && rev.status == ci.Approved {
			return true
		}
	}
	return false
}

// dimissMessage returns the dimiss message when a review is dismissed
func dismissMessage(pr *environment.Metadata, required []string) string {
	var sb strings.Builder
	sb.WriteString("new commit pushed, please re-review ")
	for _, reviewer := range required {
		sb.WriteString(fmt.Sprintf("@%s", reviewer))
	}
	return sb.String()
}

// isGithubCommit verfies GitHub is the commit author and that the commit is empty.
// Commits are checked for verification and emptiness specifically to determine if a
// pull request's reviews should be invalidated. If a commit is signed by Github and is empty
// there is no need to invalidate commits because the branch is just being updated.
func (c *Bot) isGithubCommit(ctx context.Context) error {
	signature, payloadData, err := c.getCommitVerificationParts(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	signatureFileName, err := createAndWriteTempFile(ci.Signature, signature)
	if err != nil {
		return trace.Wrap(err)
	}
	defer os.Remove(signatureFileName)

	payloadFileName, err := createAndWriteTempFile(ci.Payload, payloadData)
	if err != nil {
		return trace.Wrap(err)
	}
	defer os.Remove(payloadFileName)

	err = verifyCommit(signatureFileName, payloadFileName)
	if err != nil {
		return trace.Wrap(err)
	}
	// Verify that the content of the PR has not changed.
	return c.hasFileChange(ctx)
}

// verifyCommit verifies that a commit was made by Github's committer
// "web-flow".
//
// The go OpenPGP package (https://pkg.go.dev/golang.org/x/crypto/openpgp) is deprecated
// and has a bug in it where it can't correctly verify GPG signatures from web-flow (it
// is unclear as to why), therefore commit verifcation is being done directly on the runner.
func verifyCommit(signaturePath, payloadPath string) error {
	pathToKey, err := getGithubKey()
	if err != nil {
		return trace.Wrap(err)
	}
	defer os.Remove(pathToKey)

	err = importKey(pathToKey)
	if err != nil {
		return trace.Wrap(err)
	}
	// GPG verification command requires the signature as the first argument
	// Runner must have gpg (GnuPG) installed.
	cmd := exec.Command("/usr/bin/gpg", "--verify", signaturePath, payloadPath)
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return trace.BadParameter("commit is not verified and/or is not signed by GitHub")
		}
		return trace.Wrap(err)
	}
	return nil
}

// importKey imports a Github's public GPG key (web-flow) into the GPG keyring on the
// runner given the path.
func importKey(pathToKey string) error {
	cmd := exec.Command("/usr/bin/gpg", "--import", pathToKey)
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return trace.BadParameter("failed to import Github's web-flow key")
		}
		return trace.Wrap(err)
	}
	return nil
}

// getGithubKey gets Github's public GPG key (web-flow) and writes it to disk.
// This is the key that is used to verify that a commit was signed by them.
func getGithubKey() (string, error) {
	response, err := http.Get(ci.WebflowKeyURL)
	if err != nil {
		return "", trace.Wrap(err)
	}
	b, err := io.ReadAll(response.Body)
	if err != nil {
		return "", trace.Wrap(err)
	}
	defer response.Body.Close()
	return createAndWriteTempFile(ci.GithubKey, string(b))
}

// hasFileChange compares all of the files that have changes in the PR to the one at the current commit.
// This is used for comparing files when Github makes a auto update branch commit to ensure the merge
// didn't result in changes/additions/deletions to the files already in the PR.
func (c *Bot) hasFileChange(ctx context.Context) error {
	pr := c.Environment.Metadata
	clt := c.Environment.Client
	prFiles, _, err := clt.PullRequests.ListFiles(ctx, pr.RepoOwner, pr.RepoName, pr.Number, &github.ListOptions{})
	if err != nil {
		return trace.Wrap(err)
	}
	headCommit, _, err := clt.Repositories.GetCommit(ctx, pr.RepoOwner, pr.RepoName, pr.HeadSHA)
	if err != nil {
		return trace.Wrap(err)
	}
	for _, headFile := range headCommit.Files {
		for _, prFile := range prFiles {
			if *headFile.Filename == *prFile.Filename || *headFile.SHA == *prFile.SHA {
				return trace.BadParameter("detected file change")
			}
		}
	}
	return nil
}

// getCommitVerificationParts returns a commit signaure, the commit data to perform GPG signature verification on.
func (c *Bot) getCommitVerificationParts(ctx context.Context) (signature string, payload string, err error) {
	pr := c.Environment.Metadata
	commit, _, err := c.Environment.Client.Repositories.GetCommit(ctx, pr.RepoOwner, pr.RepoName, pr.HeadSHA)
	if err != nil {
		return "", "", trace.Wrap(err)
	}
	if commit.Commit.Verification.Signature == nil || commit.Commit.Verification.Payload == nil {
		return "", "", trace.BadParameter("commit is not signed")
	}
	return *commit.Commit.Verification.Signature, *commit.Commit.Verification.Payload, nil
}

// createAndWriteTempFile creates a temp file and writes to it.
func createAndWriteTempFile(prefix, data string) (fileName string, err error) {
	file, err := os.CreateTemp(os.TempDir(), prefix)
	if err != nil {
		return "", trace.Wrap(err)
	}
	if _, err = file.Write([]byte(data)); err != nil {
		return "", trace.Wrap(err)
	}
	if err := file.Close(); err != nil {
		return "", trace.Wrap(err)
	}
	return file.Name(), nil
}

// invalidateApprovals dismisses all approved reviews on a pull request.
func (c *Bot) invalidateApprovals(ctx context.Context, msg string, reviews map[string]review) error {
	pr := c.Environment.Metadata
	for _, v := range reviews {
		if pr.HeadSHA != v.commitID {
			_, _, err := c.Environment.Client.PullRequests.DismissReview(ctx,
				pr.RepoOwner,
				pr.RepoName,
				pr.Number,
				v.id,
				&github.PullRequestReviewDismissalRequest{Message: &msg},
			)
			if err != nil {
				return trace.Wrap(err)
			}
		}
	}
	return nil
}
