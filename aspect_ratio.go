package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
)

type Probe struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)

	var buff bytes.Buffer
	cmd.Stdout = &buff

	err := cmd.Run()
	if err != nil {
		fmt.Println(err)
		return "", err
	}

	var result Probe
	if err := json.Unmarshal(buff.Bytes(), &result); err != nil {
		return "", err
	}

	if len(result.Streams) == 0 {
		return "", errors.New("no result from probe")
	}

	width := result.Streams[0].Width
	height := result.Streams[0].Height

	ratio := float64(width) / float64(height)
	if math.Abs(ratio-(16.0/9.0)) < 0.01 {
		return "16:9", nil
	} else if math.Abs(ratio-(9.0/16.0)) < 0.01 {
		return "9:16", nil
	} else {
		return "other", nil
	}
}
