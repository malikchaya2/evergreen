package operations

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	humanize "github.com/dustin/go-humanize"
	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/manifest"
	"github.com/evergreen-ci/evergreen/rest/client"
	"github.com/evergreen-ci/evergreen/service"
	"github.com/evergreen-ci/evergreen/thirdparty"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

const defaultCloneDepth = 500
const fileNameMaxLength = 250

func Fetch() cli.Command {
	const (
		taskFlagName      = "task"
		sourceFlagName    = "source"
		artifactsFlagName = "artifacts"
		shallowFlagName   = "shallow"
		noPatchFlagName   = "patch"
		tokenFlagName     = "token"
	)

	return cli.Command{
		Name:  "fetch",
		Usage: "fetch the source or artifacts associated with a task",
		Flags: []cli.Flag{
			cli.StringFlag{
				Name:  joinFlagNames(dirFlagName, "d"),
				Usage: "root directory to fetch artifacts into. defaults to current working directory",
			},
			cli.StringFlag{
				Name:  joinFlagNames(taskFlagName, "t"),
				Usage: "task associated with the data to fetch",
			},
			cli.StringFlag{
				Name:  joinFlagNames(tokenFlagName, "k"),
				Usage: "GitHub API token",
			},
			cli.BoolFlag{
				Name:  sourceFlagName,
				Usage: "clones the source for the given task",
			},
			cli.BoolFlag{
				Name:  artifactsFlagName,
				Usage: "fetch artifacts for the task and all of its recursive dependents",
			},
			cli.BoolFlag{
				Name:  shallowFlagName,
				Usage: "don't recursively download artifacts from dependency tasks",
			},
			cli.BoolFlag{
				Name:  noPatchFlagName,
				Usage: "when using --source with a patch task, skip applying the patch",
			},
		},
		Before: mergeBeforeFuncs(
			requireClientConfig,
			setPlainLogger,
			requireStringFlag(taskFlagName),
			requireWorkingDirFlag(dirFlagName),
			func(c *cli.Context) error {
				if c.Bool(sourceFlagName) || c.Bool(artifactsFlagName) {
					return nil
				}
				return errors.New("must specify at least one of either --artifacts or --source")
			}),
		Action: func(c *cli.Context) error {
			confPath := c.Parent().String(confFlagName)
			wd := c.String(dirFlagName)
			doFetchSource := c.Bool(sourceFlagName)
			doFetchArtifacts := c.Bool(artifactsFlagName)
			taskID := c.String(taskFlagName)
			noPatch := c.Bool(noPatchFlagName)
			shallow := c.Bool(shallowFlagName)
			token := c.String(tokenFlagName)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			conf, err := NewClientSettings(confPath)
			if err != nil {
				return errors.Wrap(err, "loading configuration")
			}

			client := conf.setupRestCommunicator(ctx)
			defer client.Close()

			ac, rc, err := conf.getLegacyClients()
			if err != nil {
				return errors.Wrap(err, "setting up legacy Evergreen client")
			}

			if doFetchSource {
				if err = fetchSource(ctx, ac, rc, client, wd, taskID, token, noPatch); err != nil {
					return err
				}
			}

			if doFetchArtifacts {
				if err = fetchArtifacts(rc, taskID, wd, shallow); err != nil {
					return err
				}
			}

			return nil
		},
	}
}

////////////////////////////////////////////////////////////////////////
//
// Implementation details (legacy)

func fetchSource(ctx context.Context, ac, rc *legacyClient, comm client.Communicator,
	rootPath, taskId, token string, noPatch bool) error {
	task, err := rc.GetTask(taskId)
	if err != nil {
		return err
	}
	if task == nil {
		return errors.New("task not found")
	}
	project, err := rc.GetProject(task.Version)
	if err != nil {
		return err
	}

	pRef, err := ac.GetProjectRef(task.Project)
	if err != nil {
		return err
	}
	mfest, err := comm.GetManifestByTask(ctx, taskId)
	if err != nil && !strings.Contains(err.Error(), "no manifest found") {
		grip.Warning(message.WrapError(err, message.Fields{
			"message":       "problem getting manifest",
			"task":          taskId,
			"task_version":  task.Version,
			"task_project":  task.Project,
			"task_revision": task.Revision,
		}))
	}

	cloneDir := util.CleanForPath(fmt.Sprintf("source-%v", task.Project))
	var patch *service.RestPatch
	if evergreen.IsPatchRequester(task.Requester) {
		cloneDir = util.CleanForPath(fmt.Sprintf("source-patch-%v_%v", task.PatchNumber, task.Project))
		patch, err = rc.GetRestPatch(task.PatchId)
		if err != nil {
			return err
		}
	} else {
		if len(task.Revision) >= 5 {
			cloneDir = util.CleanForPath(fmt.Sprintf("source-%v-%v", task.Project, task.Revision[0:6]))
		}
	}
	cloneDir = filepath.Join(rootPath, cloneDir)
	err = cloneSource(task, pRef, project, cloneDir, token, mfest)
	if err != nil {
		return err
	}
	if patch != nil && !noPatch {
		err = applyPatch(patch, cloneDir, project, project.FindBuildVariant(task.BuildVariant))
		if err != nil {
			return err
		}
	}

	return nil
}

type cloneOptions struct {
	owner      string
	repository string
	revision   string
	rootDir    string
	branch     string
	token      string
	depth      uint
}

func clone(opts cloneOptions) error {
	// clone the repo first
	cloneArgs := []string{"clone", thirdparty.FormGitUrl("github.com", opts.owner, opts.repository, opts.token)}
	if opts.depth > 0 {
		cloneArgs = append(cloneArgs, "--depth", fmt.Sprintf("%d", opts.depth))
	}
	if opts.branch != "" {
		cloneArgs = append(cloneArgs, "-b", opts.branch)
	}

	cloneArgs = append(cloneArgs, opts.rootDir)
	grip.Debug(cloneArgs)

	c := exec.Command("git", cloneArgs...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	err := c.Run()
	if err != nil {
		return err
	}

	// try to check out the revision we want
	checkoutArgs := []string{"checkout", opts.revision}
	grip.Debug(checkoutArgs)

	c = exec.Command("git", checkoutArgs...)
	stdoutBuf, stderrBuf := &bytes.Buffer{}, &bytes.Buffer{}
	c.Stdout = io.MultiWriter(os.Stdout, stdoutBuf)
	c.Stderr = io.MultiWriter(os.Stderr, stderrBuf)
	c.Dir = opts.rootDir
	err = c.Run()
	if err != nil {
		if !bytes.Contains(stderrBuf.Bytes(), []byte("reference is not a tree:")) {
			return err
		}

		// we have to go deeper
		fetchArgs := []string{"fetch", "--unshallow"}
		grip.Debug(fetchArgs)

		c = exec.Command("git", fetchArgs...)
		c.Stdout, c.Stderr, c.Dir = os.Stdout, os.Stderr, opts.rootDir
		err = c.Run()
		if err != nil {
			return err
		}
		// now it's unshallow, so try again to check it out
		checkoutRetryArgs := []string{"checkout", opts.revision}
		grip.Debug(checkoutRetryArgs)

		c = exec.Command("git", checkoutRetryArgs...)
		c.Stdout, c.Stderr, c.Dir = os.Stdout, os.Stderr, opts.rootDir
		return c.Run()
	}
	return nil
}

func cloneSource(task *service.RestTask, project *model.ProjectRef, config *model.Project,
	cloneDir, token string, mfest *manifest.Manifest) error {
	// Fetch the outermost repo for the task
	err := clone(cloneOptions{
		owner:      project.Owner,
		repository: project.Repo,
		revision:   task.Revision,
		rootDir:    cloneDir,
		branch:     project.Branch,
		depth:      defaultCloneDepth,
		token:      token,
	})

	if err != nil {
		return err
	}

	// Then fetch each of the modules
	variant := config.FindBuildVariant(task.BuildVariant)
	if variant == nil {
		return errors.Errorf("finding build variant '%s' in config", task.BuildVariant)
	}

	for _, moduleName := range variant.Modules {
		module, err := config.GetModuleByName(moduleName)
		if err != nil || module == nil {
			return errors.Errorf("variant refers to a module '%s' that doesn't exist", moduleName)
		}
		revision := module.Branch
		if mfest != nil {
			mfestModule, ok := mfest.Modules[moduleName]
			if ok && mfestModule.Revision != "" {
				revision = mfestModule.Revision
			}
			if override, ok := mfest.ModuleOverrides[moduleName]; ok {
				revision = override
			}
		}

		modulePrefix := module.Prefix
		if task.ModulePaths != nil && task.ModulePaths[module.Name] != "" {
			modulePrefix = task.ModulePaths[module.Name]
		}

		moduleBase := filepath.Join(cloneDir, modulePrefix, module.Name)
		fmt.Printf("Fetching module %v at %v\n", moduleName, module.Branch)
		owner, repo, err := thirdparty.ParseGitUrl(module.Repo)
		if err != nil {
			return errors.Wrapf(err, "parsing git URL '%s'", module.Repo)
		}
		err = clone(cloneOptions{
			owner:      owner,
			repository: repo,
			revision:   revision,
			rootDir:    filepath.ToSlash(moduleBase),
			token:      token,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func applyPatch(patch *service.RestPatch, rootCloneDir string, conf *model.Project, variant *model.BuildVariant) error {
	// patch sets and contain multiple patches, some of them for modules
	for _, patchPart := range patch.Patches {
		// calling `git apply` on an empty patch exits 128
		if len(patchPart.PatchSet.Patch) == 0 {
			continue
		}

		var dir string
		if patchPart.ModuleName == "" {
			// if patch is not part of a module, just apply patch against src root
			dir = rootCloneDir
		} else {
			fmt.Println("Applying patches for module", patchPart.ModuleName)
			// if patch is part of a module, apply patch in module root
			module, err := conf.GetModuleByName(patchPart.ModuleName)
			if err != nil || module == nil {
				return errors.Wrapf(err, "finding module '%s'", patchPart.ModuleName)
			}

			// skip the module if this build variant does not use it
			if !utility.StringSliceContains(variant.Modules, module.Name) {
				continue
			}

			dir = filepath.Join(rootCloneDir, module.Prefix, module.Name)
		}

		args := []string{"apply", "--whitespace=fix"}
		applyCmd := exec.Command("git", args...)
		applyCmd.Stdout, applyCmd.Stderr, applyCmd.Dir = os.Stdout, os.Stderr, dir
		applyCmd.Stdin = bytes.NewReader([]byte(patchPart.PatchSet.Patch))
		err := applyCmd.Run()
		if err != nil {
			return err
		}
	}
	return nil
}

func fetchArtifacts(rc *legacyClient, taskId string, rootDir string, shallow bool) error {
	task, err := rc.GetTask(taskId)
	if err != nil {
		return errors.Wrapf(err, "getting task '%s'", taskId)
	}
	if task == nil {
		return errors.New("task not found")
	}

	urls, err := getUrlsChannel(rc, task, shallow)
	if err != nil {
		return errors.WithStack(err)
	}

	return errors.Wrapf(downloadUrls(rootDir, urls, 4), "downloading artifacts for task '%s'", taskId)
}

// searchDependencies does a depth-first search of the dependencies of the "seed" task, returning
// a list of all tasks related to it in the dependency graph. It performs this by doing successive
// calls to the API to crawl the graph, keeping track of any already-processed tasks in the "found"
// map.
func searchDependencies(rc *legacyClient, seed *service.RestTask, found map[string]bool) ([]*service.RestTask, error) {
	out := []*service.RestTask{}
	for _, dep := range seed.DependsOn {
		if _, ok := found[dep.TaskId]; ok {
			continue
		}
		t, err := rc.GetTask(dep.TaskId)
		if err != nil {
			return nil, err
		}
		if t != nil {
			found[t.Id] = true
			out = append(out, t)
			more, err := searchDependencies(rc, t, found)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
			for _, d := range more {
				found[d.Id] = true
			}
		}
	}
	return out, nil
}

type artifactDownload struct {
	url  string
	path string
}

func getArtifactFolderName(task *service.RestTask) string {
	if evergreen.IsPatchRequester(task.Requester) {
		return fmt.Sprintf("artifacts-patch-%v_%v_%v", task.PatchNumber, task.BuildVariant, task.DisplayName)
	}

	if len(task.Revision) >= 5 {
		return fmt.Sprintf("artifacts-%v-%v_%v", task.Revision[0:6], task.BuildVariant, task.DisplayName)
	}
	return fmt.Sprintf("artifacts-%v_%v", task.BuildVariant, task.DisplayName)
}

// getUrlsChannel takes a seed task, and returns a channel that streams all of the artifacts
// associated with the task and its dependencies. If "shallow" is set, only artifacts from the seed
// task will be streamed.
func getUrlsChannel(rc *legacyClient, seed *service.RestTask, shallow bool) (chan artifactDownload, error) {
	allTasks := []*service.RestTask{seed}
	if !shallow {
		fmt.Printf("Gathering dependencies... ")
		deps, err := searchDependencies(rc, seed, map[string]bool{})
		if err != nil {
			return nil, err
		}
		allTasks = append(allTasks, deps...)
	}
	fmt.Printf("Done.\n")

	urls := make(chan artifactDownload)
	go func() {
		for _, t := range allTasks {
			for _, f := range t.Files {
				if f.IgnoreForFetch {
					continue
				}

				directoryName := getArtifactFolderName(t)
				urls <- artifactDownload{f.URL, directoryName}
			}
		}
		close(urls)
	}()
	return urls, nil
}

func fileNameWithIndex(filename string, index int) string {
	if index-1 == 0 {
		return filename
	}
	parts := strings.Split(filename, ".")
	// If the file has no extension, just append the number with _
	if len(parts) == 1 {
		return fmt.Sprintf("%s_(%d)", filename, index-1)
	}
	// If the file has an extension, add _N (index) just before the extension.
	return fmt.Sprintf("%s_(%d).%s", parts[0], index-1, strings.Join(parts[1:], "."))
}

// truncateFilename truncates the filename (minus any extensions) so the entire filename length is less than the max
func truncateFilename(fileName string) string {
	if len(fileName) > fileNameMaxLength {
		separatorIdx := strings.Index(fileName, ".tar")
		// Check to see if there's a .tar extension somewhere in the file. Truncate the filename before ".tar".
		// Otherwise, use "." as the separator. Truncate the filename before the final ".".
		if separatorIdx == -1 {
			separatorIdx = strings.LastIndex(fileName, ".")
			if separatorIdx == -1 {
				return fileName[0:fileNameMaxLength]
			}
		}
		name := fileName[0:separatorIdx]
		extension := fileName[separatorIdx:]
		toTruncate := len(fileName) - fileNameMaxLength
		if len(name) <= toTruncate {
			return fileName
		}
		newEndIdx := len(name) - toTruncate
		name = name[0:newEndIdx]
		fileName = name + extension
	}
	return fileName
}

// downloadUrls pulls a set of artifacts from the given channel and downloads them, using up to
// the given number of workers in parallel. The given root directory determines the base location
// where all the artifact files will be downloaded to.
func downloadUrls(root string, urls chan artifactDownload, workers int) error {
	if workers <= 0 {
		panic("invalid workers count")
	}
	wg := sync.WaitGroup{}
	errs := make(chan error)
	wg.Add(workers)

	// Keep track of filenames being downloaded, so that if there are collisions, we can detect
	// and re-name the file to something else.
	fileNamesUsed := struct {
		nameCounts map[string]int
		sync.Mutex
	}{nameCounts: map[string]int{}}

	for i := 0; i < workers; i++ {
		go func(workerId int) {
			defer wg.Done()
			counter := 0
			for u := range urls {

				// Try to determinate the file location for the output.
				folder := filepath.Join(root, u.path)
				// As a backup plan in case we can't figure out the file name from the URL,
				// the file name will just be named after the worker ID and file index.
				justFile := fmt.Sprintf("%v_%v", workerId, counter)
				parsedUrl, err := url.Parse(u.url)
				if err == nil {
					// under normal operation, the file name written to disk will match the name
					// of the file in the URL. For instance, http://www.website.com/file.tgz
					// will assume "file.tgz".
					pathParts := strings.Split(parsedUrl.Path, "/")
					if len(pathParts) >= 1 {
						justFile = util.CleanForPath(pathParts[len(pathParts)-1])
					}
				}

				fileName := truncateFilename(filepath.Join(folder, justFile))
				fileNamesUsed.Lock()
				for {
					fileNamesUsed.nameCounts[fileName]++
					testFileName := fileNameWithIndex(fileName, fileNamesUsed.nameCounts[fileName])
					_, err = os.Stat(testFileName)
					if err != nil {
						if os.IsNotExist(err) {
							// we found a file name to safely create without collisions..
							fileName = testFileName
							break
						}
						// something else went wrong.
						errs <- errors.Wrapf(err, "checking if file '%s' exists", testFileName)
						return
					}
				}

				fileNamesUsed.Unlock()

				err = os.MkdirAll(folder, 0777)
				if err != nil {
					errs <- errors.Wrapf(err, "creating output directory '%s'", folder)
					continue
				}

				out, err := os.Create(fileName)
				if err != nil {
					errs <- errors.Wrapf(err, "creating file '%s'", fileName)
					continue
				}
				defer out.Close() // nolint
				resp, err := http.Get(u.url)
				if err != nil {
					errs <- errors.Wrapf(err, "downloading URL '%s'", u.url)
					continue
				}
				defer resp.Body.Close() // nolint

				// If we can get the info, determine the file size so that the human can get an
				// idea of how long the file might take to download.
				// TODO: progress bars.
				length, _ := strconv.Atoi(resp.Header.Get(evergreen.ContentLengthHeader))
				sizeLog := ""
				if length > 0 {
					sizeLog = fmt.Sprintf(" (%s)", humanize.Bytes(uint64(length)))
				}

				justFile = filepath.Base(fileName)
				fmt.Printf("(worker %d) Downloading %s to directory %s%s\n", workerId, justFile, u.path, sizeLog)
				_, err = io.Copy(out, resp.Body)
				if err != nil {
					errs <- errors.Wrapf(err, "copying body from URL '%s' to file '%s'", u.url, fileName)
					continue
				}
				counter++
			}
		}(i)
	}

	done := make(chan struct{})
	var hasErrors error
	go func() {
		defer close(done)
		for e := range errs {
			hasErrors = errors.New("some files could not be downloaded successfully")
			fmt.Println("error: ", e)
		}
	}()
	wg.Wait()
	close(errs)
	<-done

	return hasErrors
}
