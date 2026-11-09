// SPDX-License-Identifier: MIT

package processor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/boyter/gocodewalker"
)

// ProcessResult runs the same pipeline as Process but returns structured results
// instead of formatting to stdout. Useful for programmatic consumers like MCP servers.
func ProcessResult() ([]LanguageSummary, error) {
	ProcessConstants()
	processFlags()
	cleanVisitedPaths()
	resetFileErrors()

	if len(DirFilePaths) == 0 {
		DirFilePaths = append(DirFilePaths, ".")
	}

	filePaths := []string{}
	dirPaths := []string{}

	for _, f := range DirFilePaths {
		fpath := filepath.Clean(f)

		s, err := os.Stat(fpath)
		if err != nil {
			return nil, fmt.Errorf("file or directory could not be read: %s", fpath)
		}

		if s.IsDir() {
			dirPaths = append(dirPaths, fpath)
		} else {
			filePaths = append(filePaths, fpath)
		}
	}

	SortBy = strings.ToLower(SortBy)
	cancel, stopCancellation := beginScanCancellation()
	defer stopCancellation()
	ctx := processorContext{remap: newRemapConfig(RemapAll, RemapUnknown), cancel: cancel}

	printDebugF("NumCPU: %d", runtime.NumCPU())
	printDebugF("SortBy: %s", SortBy)
	printDebugF("PathDenyList: %v", PathDenyList)

	potentialFilesQueue := make(chan []*gocodewalker.File, FileListQueueSize)
	fileListQueue := make(chan *FileJob, FileListQueueSize)
	countedJobs := make(chan *FileJob, FileSummaryJobQueueSize)

	fileWalker := gocodewalker.NewParallelFileWalker(dirPaths, nil)
	fileWalker.SetFileBatchQueue(potentialFilesQueue)
	fileWalker.SetErrorHandler(func(e error) bool {
		printError(e.Error())
		return true
	})
	fileWalker.IgnoreGitIgnore = GitIgnore
	fileWalker.IgnoreIgnoreFile = Ignore
	fileWalker.IgnoreGitModules = GitModuleIgnore
	fileWalker.IncludeHidden = true
	fileWalker.ExcludeDirectory = PathDenyList
	fileWalker.SetConcurrency(DirectoryWalkerJobWorkers)

	if !SccIgnore {
		fileWalker.CustomIgnore = []string{".sccignore"}
	}
	fileWalker.CustomIgnoreFiles = IgnoreFiles

	var excludePathRegexes []*regexp.Regexp
	for _, exclude := range Exclude {
		regexpResult, err := regexp.Compile(exclude)
		if err == nil {
			fileWalker.ExcludeFilenameRegex = append(fileWalker.ExcludeFilenameRegex, regexpResult)
			fileWalker.ExcludeDirectoryRegex = append(fileWalker.ExcludeDirectoryRegex, regexpResult)
			excludePathRegexes = append(excludePathRegexes, regexpResult)
		} else {
			printError(err.Error())
		}
	}

	walkerDone := make(chan struct{})
	go func() {
		select {
		case <-cancel.signal():
			fileWalker.Terminate()
		case <-walkerDone:
		}
	}()
	go func() {
		err := fileWalker.Start()
		close(walkerDone)
		if err != nil && !errors.Is(err, gocodewalker.ErrTerminateWalk) {
			printError(err.Error())
		}
	}()

	startFileJobProducer(potentialFilesQueue, filePaths, excludePathRegexes, fileListQueue, cancel)

	go ctx.fileProcessorWorker(fileListQueue, countedJobs)

	fileSummaryJobQueue := applyDuplicateFilter(countedJobs)

	language := aggregateLanguageSummary(fileSummaryJobQueue)
	language = sortLanguageSummary(language)

	// A canceled run still summarizes everything the workers had finished, so
	// partial results come back; the error tells the caller they are partial.
	if cancel.canceled() {
		return language, context.Canceled
	}

	return language, nil
}
