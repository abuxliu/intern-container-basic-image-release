package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"bufio"
	"crypto/rand"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/gocolly/colly"
	"github.com/gocolly/colly/debug"
)

type Downloader struct {
	io.Reader
	Total   int64
	Current int64
}

func (d *Downloader) Read(p []byte) (n int, err error) {
	n, err = d.Reader.Read(p)
	d.Current += int64(n)
	fmt.Printf("\r正在下载，下载进度：%.2f%%", float64(d.Current*10000/d.Total)/100)
	if d.Current == d.Total {
		fmt.Printf("\r下载完成，下载进度：%.2f%%\n", float64(d.Current*10000/d.Total)/100)
	}
	return
}

func downloadFile(url, filePath string) {
	defer wg.Done()
	resp, err := http.Get(url)
	if err != nil {
		log.Fatalln(err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	file, err := os.Create(filePath)
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = file.Close()
	}()
	downloader := &Downloader{
		Reader: resp.Body,
		Total:  resp.ContentLength,
	}
	if _, err := io.Copy(file, downloader); err != nil {
		log.Fatalln(err)
	}
}

var wg sync.WaitGroup

type WebPageInfo struct {
	Path    string
	URL     string
	Version string
}

type DockerHubTag struct {
	Count    int64       `json:"count"`
	Next     string      `json:"next"`
	Previous interface{} `json:"previous"`
	Results  []struct {
		Creator  int64 `json:"creator"`
		FullSize int64 `json:"full_size"`
		ID       int64 `json:"id"`
		Images   []struct {
			Architecture string      `json:"architecture"`
			Digest       string      `json:"digest"`
			Features     string      `json:"features"`
			LastPulled   string      `json:"last_pulled"`
			LastPushed   string      `json:"last_pushed"`
			Os           string      `json:"os"`
			OsFeatures   string      `json:"os_features"`
			OsVersion    interface{} `json:"os_version"`
			Size         int64       `json:"size"`
			Status       string      `json:"status"`
			Variant      interface{} `json:"variant"`
		} `json:"images"`
		LastUpdated         string `json:"last_updated"`
		LastUpdater         int64  `json:"last_updater"`
		LastUpdaterUsername string `json:"last_updater_username"`
		Name                string `json:"name"`
		Repository          int64  `json:"repository"`
		TagLastPulled       string `json:"tag_last_pulled"`
		TagLastPushed       string `json:"tag_last_pushed"`
		TagStatus           string `json:"tag_status"`
		V2                  bool   `json:"v2"`
	} `json:"results"`
}

func GetOpenEulerTag() []string {
	var Result []WebPageInfo
	url := "https://repo.openeuler.org/"
	c := colly.NewCollector(colly.UserAgent("Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0.3987.163 Safari/537.36"), colly.MaxDepth(1), colly.Debugger(&debug.LogDebugger{}))
	c.OnHTML("table[id='list']", func(e *colly.HTMLElement) {
		e.ForEach("td[class='link']", func(i int, item *colly.HTMLElement) {
			var WebPageInfo WebPageInfo
			WebPageInfo.Path = item.ChildText("a")
			if MatchDockerImageDir(WebPageInfo.Path) {
				WebPageInfo.Version = strings.ToLower(WebPageInfo.Path[10 : len(WebPageInfo.Path)-1])
				WebPageInfo.URL = path.Join(url, item.ChildAttr("a", "href"))
				Result = append(Result, WebPageInfo)
			}
		})
	})
	err := c.Visit(url)
	if err != nil {
		fmt.Println(err.Error())
	}
	var Tag []string
	for i := 0; i < len(Result); i++ {
		Tag = append(Tag, Result[i].Version)
	}
	return Tag
}

func MatchDockerImageDir(Text string) bool {
	reg := regexp.MustCompile(`^openEuler-[\d].*`)
	if len(reg.FindAllString(Text, -1)) == 1 {
		return true
	} else {
		return false
	}
}

func GetDockerHubTag() []string {
	url := "https://hub.docker.com/v2/repositories/openeuler2k8s/openeuler/tags"
	method := "GET"
	client := &http.Client{}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		panic(err)
	}
	res, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		panic(err)
	}
	var DockerHubTag DockerHubTag
	err = json.Unmarshal(body, &DockerHubTag)
	if err != nil {
		panic(err)
	}
	var Tag []string
	for i := 0; i < len(DockerHubTag.Results); i++ {
		if DockerHubTag.Results[i].Name != "latest" {
			Tag = append(Tag, DockerHubTag.Results[i].Name)
		}
	}
	return Tag
}

func SelectStringInList(SrcString string, DestinationTag []string) bool {
	for i := 0; i < len(DestinationTag); i++ {
		if DestinationTag[i] == SrcString {
			return true
		}
	}
	return false
}

func MatchTag(SourceTag []string, DestinationTag []string) []string {
	var Result []string
	for i := 0; i < len(SourceTag); i++ {
		if SelectStringInList(SourceTag[i], DestinationTag) {
			continue
		} else {
			Result = append(Result, SourceTag[i])
		}
	}
	return Result
}

func PathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func sha256encode(FilePath string) string {
	f, err := os.Open(FilePath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		log.Fatal(err)
	}
	result := h.Sum(nil)
	hex_string_data := hex.EncodeToString(result)
	return hex_string_data
}

func ReadFile(FilePath string) string {
	content, err := os.ReadFile(FilePath)
	if err != nil {
		panic(err)
	}
	return string(content)[0:64]
}

func ExecCommand(Command string) string {
	fmt.Println(Command)
	cmd := exec.Command("/bin/bash", "-c", Command)
	out, err := cmd.Output()
	if err != nil {
		fmt.Println(err)
	}
	return string(out)
}

func ImagePrepare(MatchResult []string, archs []string) {
	pwd, _ := os.Getwd()
	for i := 0; i < len(MatchResult); i++ {
		for j := 0; j < len(archs); j++ {
			version := MatchResult[i]
			BasicURL := "https://repo.openeuler.org/openEuler-" + strings.ToUpper(version) + "/docker_img/"
			dir := filepath.Join(pwd, "openEuler", MatchResult[i], archs[j])
			err := os.MkdirAll(dir, 0766)
			if err != nil {
				fmt.Println(err)
			}
			imageFile := "openEuler-docker." + archs[j] + ".tar.xz"
			rootfsFile := "openEuler-docker-rootfs." + archs[j] + ".tar"
			sha256sumFile := "openEuler-docker." + archs[j] + ".tar.xz.sha256sum"
			imagePath := filepath.Join(dir, imageFile)
			sha256sumPath := filepath.Join(dir, sha256sumFile)
			rootfsPath := filepath.Join(dir, rootfsFile)
			isExist, err := PathExists(imagePath)
			if err != nil {
				panic(err)
			}
			if !isExist {
				url := BasicURL + archs[j] + "/" + imageFile
				fmt.Println(url)
				wg.Add(1)
				downloadFile(url, imagePath)
			}
			isExist, err = PathExists(sha256sumPath)
			if err != nil {
				panic(err)
			}
			if !isExist {
				url := BasicURL + archs[j] + "/" + sha256sumFile
				wg.Add(1)
				downloadFile(url, sha256sumPath)
			}
			wg.Wait()
			SrcSha256 := sha256encode(imagePath)
			DestSha256 := ReadFile(sha256sumPath)
			if SrcSha256 != DestSha256 {
				panic("Sha256 Sum Error.")
			}
			isExist, err = PathExists(rootfsPath)
			if err != nil {
				panic(err)
			}
			sysType := runtime.GOOS
			if sysType != "linux" {
				panic("Only Linux Run.")
			}
			if !isExist {
				os.Chdir(dir)
				Command := "tar -xf openEuler-docker." + archs[j] + ".tar.xz --wildcards '*.tar' --exclude 'layer.tar'"
				result := ExecCommand(Command)
				fmt.Println(result)
				Command = "ls | xargs -n1 | grep -v openEuler |grep *.tar"
				result = ExecCommand(Command)
				fmt.Println(result)
				arr := strings.Split(result, "\n")
				fmt.Println(arr)
				tarFileName := arr[0]
				Command = "mv " + tarFileName + " openEuler-docker-rootfs." + archs[j] + ".tar"
				result = ExecCommand(Command)
				fmt.Println(result)
				Command = "xz -z openEuler-docker-rootfs." + archs[j] + ".tar"
				result = ExecCommand(Command)
				fmt.Println(result)
				Command = "cp " + pwd + "/Dockerfile " + dir + "/Dockerfile"
				result = ExecCommand(Command)
				fmt.Println(result)
			}
		}
	}
}

func PullAnImage() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	authConfig := types.AuthConfig{
		Username: "openeuler2k8s",
		Password: "changeme",
	}

	encodedJSON, err := json.Marshal(authConfig)
	if err != nil {
		panic(err)
	}
	authStr := base64.URLEncoding.EncodeToString(encodedJSON)

	out, err := cli.ImagePull(ctx, "alpine", types.ImagePullOptions{RegistryAuth: authStr})
	if err != nil {
		panic(err)
	}

	defer out.Close()
	io.Copy(os.Stdout, out)
	opt := types.ImageBuildOptions{
		CPUSetCPUs:   "2",
		CPUSetMems:   "12",
		CPUShares:    20,
		CPUQuota:     10,
		CPUPeriod:    30,
		Memory:       256,
		MemorySwap:   512,
		ShmSize:      10,
		CgroupParent: "cgroup_parent",
		Dockerfile:   "dockerSrc/docker-debug-container/Dockerfile",
	}
	cli.ImageBuild(ctx, nil, opt)
}

func ListImage() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	images, err := cli.ImageList(ctx, types.ImageListOptions{})
	if err != nil {
		panic(err)
	}

	for _, image := range images {
		fmt.Println(image.RepoTags)
	}
}

func run() {
	var archs []string
	archs = append(archs, "x86_64")
	archs = append(archs, "aarch64")
	OpenEulerTag := GetOpenEulerTag()
	DockerHubTag := GetDockerHubTag()
	MatchResult := MatchTag(OpenEulerTag, DockerHubTag)
	ImagePrepare(MatchResult, archs)
}

func main() {
	run()
	// PullAnImage()
	if len(os.Args) != 3 {
		fmt.Println("bad num of arguments:\n\t1. = dir with image content\n\t2. = image name")
		os.Exit(0)
	}

	msg, err := buildImage(os.Args[1], os.Args[2])
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(msg)
}

func createTar(srcDir, tarFIle string) error {
	/* #nosec */
	c := exec.Command("tar", "-cf", tarFIle, "-C", srcDir, ".")
	if err := c.Run(); err != nil {
		return nil
	}
	return nil
}

func tempFileName(prefix, suffix string) (string, error) {
	randBytes := make([]byte, 16)
	if _, err := rand.Read(randBytes); err != nil {
		return "", err
	}

	return filepath.Join(os.TempDir(), prefix+hex.EncodeToString(randBytes)+suffix), nil
}

func buildImage(dir, name string) ([]string, error) {

	tarFile, err := tempFileName("docker-", ".image")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tarFile)

	if err := createTar(dir, tarFile); err != nil {
		return nil, err
	}

	/* #nosec */
	dockerFileTarReader, err := os.Open(tarFile)
	if err != nil {
		return nil, err
	}
	defer dockerFileTarReader.Close()

	cli, err := client.NewEnvClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(300)*time.Second)
	defer cancel()

	buildArgs := make(map[string]*string)

	PWD, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	defer os.Chdir(PWD)

	if err := os.Chdir(dir); err != nil {
		return nil, err
	}

	resp, err := cli.ImageBuild(
		ctx,
		dockerFileTarReader,
		types.ImageBuildOptions{
			Dockerfile: "./Dockerfile",
			Tags:       []string{name},
			NoCache:    true,
			Remove:     true,
			BuildArgs:  buildArgs,
		})

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var messages []string

	rd := bufio.NewReader(resp.Body)
	for {
		n, _, err := rd.ReadLine()
		if err != nil && err == io.EOF {
			break
		} else if err != nil {
			return messages, err
		}
		messages = append(messages, string(n))
	}

	return messages, nil
}
