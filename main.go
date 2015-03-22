package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

var (
	numWorkers     = flag.Int("n", 4, "number of workers")
	preset         = flag.String("p", "medium", "h264 preset")
	crf            = flag.String("c", "19", "h264 crf setting")
	deleteOriginal = flag.Bool("d", false, "delete original files")
	recursive      = flag.Bool("r", false, "recursive")
	outputDir      = flag.String("o", "", "output directory")
)

type FfprobeOutput struct {
	Streams []StreamInfo `json:"streams"`
	Format  FormatInfo   `json:"format"`
}

type StreamInfo struct {
	Index         int    `json:"index"`
	CodecName     string `json:"codec_name"`
	CodecLongName string `json:"codec_long_name"`
	CodecType     string `json:"codec_type"`
}

type FormatInfo struct {
	Filename       string `json:"filename"`
	FormatName     string `json:"format_name"`
	FormatLongName string `json:"format_long_name"`
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"%v [flags] input_file1 input_file2...\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
	}

	inputs := flag.Args()
	filesToConvert := make([]string, 0)

	for _, input := range inputs {
		subFiles := gatherFiles(input, *recursive)
		for _, entry := range subFiles {
			filesToConvert = append(filesToConvert, entry)
		}
	}

	var wg sync.WaitGroup
	fileChan := make(chan string)

	for i := 0; i < *numWorkers; i++ {
		wg.Add(1)
		go convertWorker(fileChan, &wg)
	}

	for _, file := range filesToConvert {
		fileChan <- file
	}

	close(fileChan)
	wg.Wait()
}

func gatherFiles(root string, recursive bool) []string {
	toExplore := make([]string, 0)
	toExplore = append(toExplore, root)
	files := make([]string, 0)
	depth1 := true

	for len(toExplore) > 0 {
		current := toExplore[len(toExplore)-1]
		toExplore = toExplore[:len(toExplore)-1]
		info, err := os.Stat(current)
		if err != nil {
			log.Fatalf("unable to stat path: %s", err)
		}
		if info.IsDir() && (recursive || depth1) {
			entries, err := ioutil.ReadDir(current)
			if err != nil {
				log.Fatalf("unable to read dir: %s", err)
			}
			for _, entry := range entries {
				toExplore = append(toExplore, path.Join(current, entry.Name()))
			}
			depth1 = false
		} else if !info.IsDir() {
			files = append(files, current)
		}
	}
	return files
}

func convertWorker(fileChan <-chan string, wg *sync.WaitGroup) {
	defer wg.Done()
	for filepath := range fileChan {
		_, err := convert(filepath)
		if err != nil {
			log.Printf("error converting: %s", err)
		}
	}
}

func getInfo(path string) FfprobeOutput {
	cmdArgs := []string{"-v", "quiet"}
	cmdArgs = append(cmdArgs, "-print_format", "json")
	cmdArgs = append(cmdArgs, "-show_format")
	cmdArgs = append(cmdArgs, "-show_streams")
	cmdArgs = append(cmdArgs, path)

	out, err := exec.Command("ffprobe", cmdArgs...).Output()

	if err != nil {
		log.Fatalf("unable to ffprobe input: %s", err)
	}

	var info FfprobeOutput
	err = json.Unmarshal(out, &info)

	if err != nil {
		log.Fatalf("unable to unmarshal ffprobe output: %s", err)
	}

	return info
}

func convert(path string) (outpath string, err error) {
	info := getInfo(path)
	var audioCodec string
	var videoCodec string

	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			audioCodec = stream.CodecName
		case "video":
			videoCodec = stream.CodecName
		}
	}

	outputFilename := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".mp4"
	if *outputDir == "" {
		outpath = filepath.Join(filepath.Dir(path), outputFilename)
	} else {
		outpath = filepath.Join(*outputDir, outputFilename)
	}

	if audioCodec == "aac" && videoCodec == "h264" && filepath.Ext(path) == ".mp4" {
		log.Printf("Conversion unneccessary for %s", path)
		if *deleteOriginal {
			err = os.Rename(path, outpath)
		} else {
			err = Copy(path, outpath)
		}
		if err != nil {
			return path, err
		}
		return outpath, nil
	}

	cmdArgs := []string{"-i", path}
	cmdArgs = append(cmdArgs, "-c:v", "libx264", "-crf", *crf, "-preset", *preset)
	cmdArgs = append(cmdArgs, "-c:a", "aac", "-strict", "experimental")
	cmdArgs = append(cmdArgs, "-b:a", "192k", "-ac", "2")
	cmdArgs = append(cmdArgs, outpath)

	log.Printf("Converting %s to %s...", path, outpath)
	cmd := exec.Command("ffmpeg", cmdArgs...)
	err = cmd.Run()
	if err == nil {
		log.Printf("Finished converting %s to %s.", path, outpath)
		if *deleteOriginal {
			log.Printf("Removing original")
			os.Remove(path)
		}
	} else {
		log.Printf("Unable to convert file %s: %s", path, err)
	}
	return
}

func Copy(src, dst string) error {
	if src == dst {
		return nil
	}
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	// no need to check errors on read only file, we already got everything
	// we need from the filesystem, so nothing can go wrong now.
	defer s.Close()
	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(d, s); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}
