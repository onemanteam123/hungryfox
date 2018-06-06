package repo

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/AlexAkulov/hungryfox"

	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/format/diff"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

type Repo struct {
	DiffChannel      chan<- *hungryfox.Diff
	HistoryPastLimit time.Time
	DataPath         string
	RepoPath         string
	URL              string

	repository     *git.Repository
	scannedHash    map[string]struct{}
	commitsTotal   int
	commitsScanned int
}

func (r *Repo) GetProgress() int {
	if r.commitsTotal > 0 {
		return (r.commitsScanned / r.commitsTotal) * 1000
	}
	return -1
}

func (r *Repo) Close() error {
	r.repository = nil // ???
	runtime.GC()       // ???
	return nil
}

func (r *Repo) SetRefs(refs []string) {
	r.scannedHash = map[string]struct{}{}
	for _, hash := range refs {
		r.scannedHash[hash] = struct{}{}
	}
}

func (r *Repo) GetRefs() (refsMap []string) {
	refsMap = []string{}
	if r.repository == nil {
		if err := r.Open(); err != nil {
			return
		}
	}
	refs, err := r.repository.References()
	if err != nil {
		return
	}
	refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Hash().IsZero() {
			return nil
		}
		if strings.HasPrefix(ref.Name().String(), "refs/keep-around/") {
			return nil
		}
		refsMap = append(refsMap, ref.Hash().String())
		return nil
	})
	lastCommit := r.getLastCommit()
	if lastCommit != "" {
		refsMap = append(refsMap, lastCommit)
	}
	return
}

func (r *Repo) isChecked(commitHash string) bool {
	_, ok := r.scannedHash[commitHash]
	return ok
}

func (r *Repo) getLastCommit() string {
	oldWD, err := os.Getwd()
	if err != nil {
		return ""
	}
	if err := os.Chdir(r.fullRepoPath()); err != nil {
		return ""
	}
	// --topo-order???
	out, err := exec.Command("git", "rev-list", "--all", "--remotes", "--date-order", "--max-count=1").Output()
	os.Chdir(oldWD)
	if err != nil {
		return ""
	}
	commits := strings.Split(string(out), "\n")
	if len(commits) > 0 {
		return commits[0]
	}
	return ""
}

func (r *Repo) getRevList() (result []*object.Commit, err error) {
	oldWD, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("error on get working dir: %v", err)
	}
	if err := os.Chdir(r.fullRepoPath()); err != nil {
		return nil, fmt.Errorf("error on change dir to %s: %v", r.fullRepoPath(), err)
	}
	// --topo-order???
	out, err := exec.Command("git", "rev-list", "--all", "--remotes", "--date-order").Output()
	os.Chdir(oldWD)
	if err != nil {
		return nil, err
	}

	hashList := strings.Split(string(out), "\n")
	for _, commitHash := range hashList {
		commitHash = strings.TrimSpace(commitHash)
		if r.isChecked(commitHash) {
			break
		}
		commit, err := r.repository.CommitObject(plumbing.NewHash(commitHash))
		if err != nil {
			continue
		}
		if commit.NumParents() != 1 {
			continue
		}
		result = append(result, commit)
	}

	r.commitsTotal = len(result)
	return result, nil
}

func (r *Repo) Open() error {
	var err error
	r.repository, err = git.PlainOpen(r.fullRepoPath())
	return err
}

// Scan - rt
func (r *Repo) Scan() error {
	commits, err := r.getRevList()
	if err != nil {
		return err
	}
	for i, commit := range commits {
		r.commitsScanned = i + 1
		if commit.Committer.When.Before(r.HistoryPastLimit) {
			r.getAllChanges(commit)
			break
		}
		r.getCommitChanges(commit)
	}
	return nil
}

func (r *Repo) getAllChanges(commit *object.Commit) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	changes, err := object.DiffTree(nil, tree)
	if err != nil {
		return err
	}
	patch, err := changes.Patch()
	if err != nil {
		return err
	}
	for _, p := range patch.FilePatches() {
		_, f := p.Files()
		if f == nil || p.IsBinary() {
			continue
		}
		for _, chunk := range p.Chunks() {
			if chunk.Type() != diff.Add {
				continue
			}
			r.DiffChannel <- &hungryfox.Diff{
				CommitHash:  commit.Hash.String(),
				RepoURL:     r.URL,
				RepoPath:    r.RepoPath,
				FilePath:    f.Path(),
				LineBegin:   0, // TODO: await https://github.com/src-d/go-git/issues/806
				Content:     chunk.Content(),
				Author:      "unknown", // TODO: Use blame for this
				AuthorEmail: "unknown",
				TimeStamp:   commit.Author.When,
			}
		}
	}
	return nil
}

func (r *Repo) getCommitChanges(commit *object.Commit) error {
	if commit == nil {
		return nil
	}
	parrentCommit, err := commit.Parent(0)
	if err != nil {
		return err
	}
	patch, err := parrentCommit.Patch(commit)
	if err != nil {
		return err
	}
	for _, p := range patch.FilePatches() {
		_, f := p.Files()
		if f == nil || p.IsBinary() {
			continue
		}
		for _, chunk := range p.Chunks() {
			if chunk.Type() != diff.Add {
				continue
			}
			r.DiffChannel <- &hungryfox.Diff{
				CommitHash:  commit.Hash.String(),
				RepoURL:     r.URL,
				RepoPath:    r.RepoPath,
				FilePath:    f.Path(),
				LineBegin:   0, // TODO: await https://github.com/src-d/go-git/issues/806
				Content:     chunk.Content(),
				Author:      commit.Author.Name,
				AuthorEmail: commit.Author.Email,
				TimeStamp:   commit.Author.When,
			}
		}
	}
	return nil
}
