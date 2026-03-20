package app

import (
	"errors"
	"reflect"
	"testing"
)

func TestRunSequentialUploadPipelineProcessesFilesSequentially(t *testing.T) {
	files := []sourceFile{
		{RelPath: "a.bin"},
		{RelPath: "b.bin"},
	}
	events := make([]string, 0, len(files)*2)

	err := runSequentialUploadPipeline(
		func(onFile func(sourceFile) error) error {
			for _, file := range files {
				if err := onFile(file); err != nil {
					return err
				}
			}
			return nil
		},
		func(file sourceFile, index int64) (*activeUpload, error) {
			events = append(events, "start:"+file.RelPath)
			return &activeUpload{file: file, index: index}, nil
		},
		func(upload *activeUpload) error {
			events = append(events, "finish:"+upload.file.RelPath)
			return nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("runSequentialUploadPipeline error: %v", err)
	}

	want := []string{
		"start:a.bin",
		"finish:a.bin",
		"start:b.bin",
		"finish:b.bin",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestRunSequentialUploadPipelineCancelsActiveUploadOnFinishError(t *testing.T) {
	wantErr := errors.New("boom")
	var cancelled string

	err := runSequentialUploadPipeline(
		func(onFile func(sourceFile) error) error {
			return onFile(sourceFile{RelPath: "a.bin"})
		},
		func(file sourceFile, index int64) (*activeUpload, error) {
			return &activeUpload{
				file:     file,
				index:    index,
				fileKey:  "1:a.bin",
				uploadID: "upload-1",
			}, nil
		},
		func(upload *activeUpload) error {
			return wantErr
		},
		func(upload *activeUpload) {
			cancelled = upload.file.RelPath
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if cancelled != "a.bin" {
		t.Fatalf("cancelled = %q, want a.bin", cancelled)
	}
}
