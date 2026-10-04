package guest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Pin resolves the latest Alpine minirootfs and kernel package for the
// pinned branch, downloads them to compute their hashes, looks up the
// latest agent versions, and writes a new versions.yaml. A maintainer
// reviews the diff and commits it; users never run this.
func Pin(ctx context.Context, out string) error {
	cur, err := PinnedVersions()
	if err != nil {
		return err
	}
	mirror := "https://dl-cdn.alpinelinux.org/alpine"
	branch := cur.Alpine.Branch
	base := mirror + "/" + branch + "/releases/aarch64/"

	rel, err := get(ctx, base+"latest-releases.yaml")
	if err != nil {
		return err
	}
	var releases []struct {
		File   string `yaml:"file"`
		SHA256 string `yaml:"sha256"`
	}
	if err := yaml.Unmarshal(rel, &releases); err != nil {
		return err
	}
	for _, r := range releases {
		if strings.HasPrefix(r.File, "alpine-minirootfs-") {
			cur.Alpine.Minirootfs, cur.Alpine.MinirootfsSHA256 = r.File, r.SHA256
		}
	}

	idx, err := get(ctx, mirror+"/"+branch+"/main/aarch64/APKINDEX.tar.gz")
	if err != nil {
		return err
	}
	ver := apkVersion(idx, "linux-virt")
	if ver == "" {
		return fmt.Errorf("linux-virt not found in APKINDEX")
	}
	cur.Alpine.KernelAPK = "linux-virt-" + ver + ".apk"
	fmt.Println("llok pin: hashing", cur.Alpine.KernelAPK)
	sum, err := getSHA256(ctx, mirror+"/"+branch+"/main/aarch64/"+cur.Alpine.KernelAPK)
	if err != nil {
		return err
	}
	cur.Alpine.KernelAPKSHA256 = sum

	for name, pkg := range map[string]string{"codex": "@openai/codex", "gemini-cli": "@google/gemini-cli"} {
		if v, err := npmLatest(ctx, pkg); err == nil {
			cur.Agents[name] = v
		} else {
			fmt.Println("llok pin:", name, "kept at", cur.Agents[name], "(", err, ")")
		}
	}
	if v, err := claudeLatest(ctx); err == nil {
		cur.Agents["claude-code"] = v
	} else {
		fmt.Println("llok pin: claude-code kept at", cur.Agents["claude-code"], "(", err, ")")
	}

	var b strings.Builder
	b.WriteString("# Pinned inputs of the guest image. Every build uses exactly these, so two\n")
	b.WriteString("# builds produce the same image and the published hashes mean something.\n")
	b.WriteString("# Update deliberately with: llok guest pin   (then review and commit).\n")
	data, _ := yaml.Marshal(cur)
	b.Write(data)
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Println("llok pin: wrote", out)
	fmt.Print(b.String())
	return nil
}

func get(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func getSHA256(ctx context.Context, url string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// apkVersion finds V: for package P: in a gzipped APKINDEX tarball by
// scanning the decompressed text (the index is small).
func apkVersion(tgz []byte, pkg string) string {
	text := gunzipAll(tgz)
	re := regexp.MustCompile(`(?m)^P:` + regexp.QuoteMeta(pkg) + `\n(?:[A-Z]:.*\n)*?V:([^\n]+)`)
	if m := re.FindSubmatch(text); m != nil {
		return string(m[1])
	}
	return ""
}

func npmLatest(ctx context.Context, pkg string) (string, error) {
	data, err := get(ctx, "https://registry.npmjs.org/"+pkg+"/latest")
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &v); err != nil || v.Version == "" {
		return "", fmt.Errorf("no version for %s", pkg)
	}
	return v.Version, nil
}

func claudeLatest(ctx context.Context) (string, error) {
	// The native installer resolves "latest" from this file.
	data, err := get(ctx, "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d8f1e4a3e0b0/claude-code-releases/latest")
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
		return "", fmt.Errorf("unexpected latest marker %q", v)
	}
	return v, nil
}
