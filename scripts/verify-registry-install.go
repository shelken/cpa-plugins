package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Artifact struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type InstallPlan struct {
	Type      string     `json:"type"`
	Artifacts []Artifact `json:"artifacts"`
}

type Plugin struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Author      string       `json:"author"`
	Version     string       `json:"version"`
	Repository  string       `json:"repository"`
	Install     *InstallPlan `json:"install,omitempty"`
}

type Registry struct {
	SchemaVersion int      `json:"schema_version"`
	Plugins       []Plugin `json:"plugins"`
}

func main() {
	registryURL := "https://raw.githubusercontent.com/shelken/cpa-plugins/main/registry.json"
	fmt.Printf("[1/5] Fetching live registry from: %s\n", registryURL)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(registryURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED to fetch registry: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "FAILED: registry returned status %s\n", resp.Status)
		os.Exit(1)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED to read response body: %v\n", err)
		os.Exit(1)
	}

	var reg Registry
	if err := json.Unmarshal(body, &reg); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED to parse registry JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("      Registry schema_version: %d, total plugins: %d\n", reg.SchemaVersion, len(reg.Plugins))

	var echoProbe *Plugin
	var codexComp *Plugin
	for i := range reg.Plugins {
		p := &reg.Plugins[i]
		if p.ID == "echo-probe" {
			echoProbe = p
		}
		if p.ID == "codexcomp" {
			codexComp = p
		}
	}

	if codexComp == nil {
		fmt.Fprintf(os.Stderr, "FAILED: codexcomp not found in registry\n")
		os.Exit(1)
	}
	fmt.Printf("[2/5] Verified external plugin: %s (v%s) from %s\n", codexComp.ID, codexComp.Version, codexComp.Repository)

	if echoProbe == nil {
		fmt.Fprintf(os.Stderr, "FAILED: echo-probe not found in registry\n")
		os.Exit(1)
	}
	if echoProbe.Install == nil || echoProbe.Install.Type != "direct" {
		fmt.Fprintf(os.Stderr, "FAILED: echo-probe missing direct install plan\n")
		os.Exit(1)
	}

	fmt.Printf("[3/5] Verified in-tree plugin: %s (v%s), install type: %s\n", echoProbe.ID, echoProbe.Version, echoProbe.Install.Type)
	fmt.Printf("      Target platforms declared: %d\n", len(echoProbe.Install.Artifacts))

	for _, artifact := range echoProbe.Install.Artifacts {
		fmt.Printf("[4/5] Downloading artifact for %s/%s...\n", artifact.GOOS, artifact.GOARCH)
		fmt.Printf("      URL: %s\n", artifact.URL)

		artResp, err := client.Get(artifact.URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED to download artifact: %v\n", err)
			os.Exit(1)
		}
		defer artResp.Body.Close()

		if artResp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "FAILED: artifact download status: %s\n", artResp.Status)
			os.Exit(1)
		}

		zipBytes, err := io.ReadAll(artResp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED to read artifact bytes: %v\n", err)
			os.Exit(1)
		}

		hash := sha256.Sum256(zipBytes)
		computedHash := hex.EncodeToString(hash[:])
		if !strings.EqualFold(computedHash, artifact.SHA256) {
			fmt.Fprintf(os.Stderr, "FAILED: checksum mismatch for %s/%s: computed=%s, declared=%s\n",
				artifact.GOOS, artifact.GOARCH, computedHash, artifact.SHA256)
			os.Exit(1)
		}
		fmt.Printf("      SHA256 checksum matched: %s\n", computedHash)

		// 5. Inspect zip content and ELF header
		zipReader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED to open zip archive: %v\n", err)
			os.Exit(1)
		}

		var soFile *zip.File
		for _, f := range zipReader.File {
			if f.Name == "echo-probe.so" {
				soFile = f
				break
			}
		}

		if soFile == nil {
			fmt.Fprintf(os.Stderr, "FAILED: echo-probe.so not found inside zip archive\n")
			os.Exit(1)
		}

		soReader, err := soFile.Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED to read echo-probe.so: %v\n", err)
			os.Exit(1)
		}
		defer soReader.Close()

		soBytes, err := io.ReadAll(soReader)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED to extract echo-probe.so bytes: %v\n", err)
			os.Exit(1)
		}

		elfFile, err := elf.NewFile(bytes.NewReader(soBytes))
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED: echo-probe.so is not a valid ELF binary: %v\n", err)
			os.Exit(1)
		}
		defer elfFile.Close()

		if elfFile.Type != elf.ET_DYN {
			fmt.Fprintf(os.Stderr, "FAILED: echo-probe.so ELF type is not ET_DYN (shared library): %v\n", elfFile.Type)
			os.Exit(1)
		}

		var expectedMachine elf.Machine
		switch artifact.GOARCH {
		case "amd64":
			expectedMachine = elf.EM_X86_64
		case "arm64":
			expectedMachine = elf.EM_AARCH64
		default:
			expectedMachine = elf.EM_NONE
		}

		if elfFile.Machine != expectedMachine {
			fmt.Fprintf(os.Stderr, "FAILED: ELF machine architecture mismatch: got %v, want %v\n", elfFile.Machine, expectedMachine)
			os.Exit(1)
		}

		fmt.Printf("      ELF validation PASSED: %s (Type: ET_DYN, Machine: %v, Size: %d bytes)\n",
			soFile.Name, elfFile.Machine, soFile.UncompressedSize64)
	}

	fmt.Println("\n[5/5] ALL VERIFICATIONS PASSED SUCCESSFULLY!")
	fmt.Println("The distribution pipeline (Registry JSON -> Direct URL -> GitHub Release -> Zip Archive -> Linux ELF .so) is 100% verified.")
}
