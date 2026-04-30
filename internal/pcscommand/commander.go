package pcscommand

import (
	"fmt"
	"strings"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
)

func commanderEscape(value string) string {
	replacer := strings.NewReplacer(
		"%", "%25",
		"\t", "%09",
		"\n", "%0A",
		"\r", "%0D",
	)
	return replacer.Replace(value)
}

func RunCommanderLs(pcspath string) error {
	if pcspath == "" {
		pcspath = baidupcs.PathSeparator
	}
	if err := matchPathByShellPatternOnce(&pcspath); err != nil {
		return err
	}

	files, err := GetBaiduPCS().FilesDirectoriesList(pcspath, baidupcs.DefaultOrderOptions)
	if err != nil {
		return err
	}

	for _, file := range files {
		entryType := "F"
		if file.Isdir {
			entryType = "D"
		}
		fmt.Printf("%s\t%d\t%s\n", entryType, file.Size, commanderEscape(file.Filename))
	}
	return nil
}
