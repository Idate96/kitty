package utils

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// countOpenFDs returns the number of entries in /proc/self/fd. The count is
// consistent across calls (always includes the fd used by ReadDir itself),
// making before/after comparisons reliable for leak detection.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot read /proc/self/fd: %v", err)
	}
	return len(entries)
}

// openTestDir opens dir as an *os.File for use with the *At family of calls.
func openTestDir(t *testing.T, dir string) *os.File {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// createTestTree builds a three-level directory tree for CopyFolderContents tests:
//
//	<root>/
//	  file1.txt                               content: "file1"
//	  link_to_file1 -> file1.txt
//	  subdir1/
//	    file2.txt                             content: "file2"
//	    subsubdir/
//	      file3.txt                           content: "file3"
//	      link_to_root_file -> ../../file1.txt
//	  subdir2/
//	    link_to_subdir1 -> ../subdir1
func createTestTree(t *testing.T, root string) {
	t.Helper()
	mustMkdir := func(p string) {
		t.Helper()
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(p, data string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustSymlink := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(root, "file1.txt"), "file1")
	mustSymlink("file1.txt", filepath.Join(root, "link_to_file1"))
	mustMkdir(filepath.Join(root, "subdir1"))
	mustMkdir(filepath.Join(root, "subdir2"))
	mustWrite(filepath.Join(root, "subdir1", "file2.txt"), "file2")
	mustMkdir(filepath.Join(root, "subdir1", "subsubdir"))
	mustSymlink("../subdir1", filepath.Join(root, "subdir2", "link_to_subdir1"))
	mustWrite(filepath.Join(root, "subdir1", "subsubdir", "file3.txt"), "file3")
	mustSymlink("../../file1.txt", filepath.Join(root, "subdir1", "subsubdir", "link_to_root_file"))
}

// readFileInDir reads the content of name relative to root (following symlinks).
func readFileInDir(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// collectEntries returns a sorted list of all paths inside root.
func collectEntries(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel != "." {
			entries = append(entries, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return entries
}

// --- basic API tests ---

func TestMkdirAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := MkdirAt(parent, "newdir", 0o755); err != nil {
		t.Fatalf("MkdirAt failed: %v", err)
	}
	info, err := os.Stat(filepath.Join(tmpDir, "newdir"))
	if err != nil || !info.IsDir() {
		t.Fatal("directory was not created")
	}

	// Creating the same directory again must return an error wrapping EEXIST.
	err = MkdirAt(parent, "newdir", 0o755)
	if err == nil {
		t.Fatal("expected error when directory already exists")
	}
	if !errors.Is(err, unix.EEXIST) {
		t.Fatalf("expected EEXIST, got %v", err)
	}
}

func TestOpenAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := OpenAt(parent, "hello.txt")
	if err != nil {
		t.Fatalf("OpenAt failed: %v", err)
	}
	defer f.Close()

	buf := make([]byte, 5)
	if _, err := f.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(buf))
	}

	if _, err := OpenAt(parent, "nonexistent"); err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestOpenDirAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := os.Mkdir(filepath.Join(tmpDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDirAt(parent, "sub")
	if err != nil {
		t.Fatalf("OpenDirAt failed: %v", err)
	}
	d.Close()

	// Opening a regular file with OpenDirAt must fail.
	if err := os.WriteFile(filepath.Join(tmpDir, "reg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDirAt(parent, "reg"); err == nil {
		t.Fatal("expected error when opening regular file as directory")
	}
}

func TestSymlinkAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := SymlinkAt(parent, "mylink", "/some/target"); err != nil {
		t.Fatalf("SymlinkAt failed: %v", err)
	}
	target, err := os.Readlink(filepath.Join(tmpDir, "mylink"))
	if err != nil {
		t.Fatalf("Readlink failed: %v", err)
	}
	if target != "/some/target" {
		t.Fatalf("expected '/some/target', got %q", target)
	}
}

func TestCreateAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	f, err := CreateAt(parent, "newfile.txt")
	if err != nil {
		t.Fatalf("CreateAt failed: %v", err)
	}
	if _, err := f.Write([]byte("test content")); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	data, err := os.ReadFile(filepath.Join(tmpDir, "newfile.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test content" {
		t.Fatalf("expected 'test content', got %q", string(data))
	}

	// CreateAt on an existing file must truncate it.
	f, err = CreateAt(parent, "newfile.txt")
	if err != nil {
		t.Fatalf("CreateAt on existing file failed: %v", err)
	}
	f.Close()
	data, _ = os.ReadFile(filepath.Join(tmpDir, "newfile.txt"))
	if len(data) != 0 {
		t.Fatalf("expected empty file after truncation, got %d bytes", len(data))
	}
}

func TestCreateDirAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	d, err := CreateDirAt(parent, "newdir", 0o755)
	if err != nil {
		t.Fatalf("CreateDirAt failed: %v", err)
	}
	d.Close()

	info, err := os.Stat(filepath.Join(tmpDir, "newdir"))
	if err != nil || !info.IsDir() {
		t.Fatal("directory was not created")
	}

	// CreateDirAt on an already-existing directory must succeed (open and return it).
	d, err = CreateDirAt(parent, "newdir", 0o755)
	if err != nil {
		t.Fatalf("CreateDirAt on existing dir failed: %v", err)
	}
	d.Close()
}

func TestStatAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(tmpDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	// StatAt follows symlinks: link.txt must appear as a regular file.
	info, err := StatAt(parent, "link.txt")
	if err != nil {
		t.Fatalf("StatAt failed: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("StatAt should follow symlinks")
	}
	if !info.Mode().IsRegular() {
		t.Fatal("expected regular file after following symlink")
	}
}

func TestLstatAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(tmpDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	// LstatAt must NOT follow symlinks.
	info, err := LstatAt(parent, "link.txt")
	if err != nil {
		t.Fatalf("LstatAt failed: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("LstatAt should report symlink, not its target")
	}

	info, err = LstatAt(parent, "file.txt")
	if err != nil {
		t.Fatalf("LstatAt on regular file failed: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("expected regular file")
	}
}

func TestUnlinkAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	path := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UnlinkAt(parent, "file.txt"); err != nil {
		t.Fatalf("UnlinkAt failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file should have been removed")
	}

	// UnlinkAt must also remove symlinks.
	if err := os.Symlink("/target", filepath.Join(tmpDir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := UnlinkAt(parent, "link"); err != nil {
		t.Fatalf("UnlinkAt on symlink failed: %v", err)
	}
}

func TestRemoveDirAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := os.Mkdir(filepath.Join(tmpDir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDirAt(parent, "empty"); err != nil {
		t.Fatalf("RemoveDirAt failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "empty")); !os.IsNotExist(err) {
		t.Fatal("directory should have been removed")
	}

	// Non-empty directory must fail.
	if err := os.Mkdir(filepath.Join(tmpDir, "nonempty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "nonempty", "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDirAt(parent, "nonempty"); err == nil {
		t.Fatal("expected error for non-empty directory")
	}
}

func TestLinkAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := os.WriteFile(filepath.Join(tmpDir, "original.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LinkAt(parent, "original.txt", parent, "hardlink.txt", false); err != nil {
		t.Fatalf("LinkAt failed: %v", err)
	}

	origInfo, _ := os.Stat(filepath.Join(tmpDir, "original.txt"))
	linkInfo, _ := os.Stat(filepath.Join(tmpDir, "hardlink.txt"))
	if origInfo.Sys().(*syscall.Stat_t).Ino != linkInfo.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("hardlink should share inode with original")
	}
}

func TestDupFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	dup, err := DupFile(f)
	if err != nil {
		t.Fatalf("DupFile failed: %v", err)
	}
	defer dup.Close()

	buf := make([]byte, 5)
	if _, err := dup.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(buf))
	}

	// Closing the original must not affect the duplicate.
	f.Close()
	if _, err := dup.Seek(0, 0); err != nil {
		t.Fatalf("seek on dup after original closed: %v", err)
	}
	buf2 := make([]byte, 5)
	if _, err := dup.Read(buf2); err != nil {
		t.Fatalf("read on dup after original closed: %v", err)
	}
	if string(buf2) != "hello" {
		t.Fatalf("dup content after closing original: expected 'hello', got %q", string(buf2))
	}
}

func TestReadLinkAt(t *testing.T) {
	tmpDir := t.TempDir()
	parent := openTestDir(t, tmpDir)
	defer parent.Close()

	if err := os.Symlink("/some/target/path", filepath.Join(tmpDir, "mylink")); err != nil {
		t.Fatal(err)
	}
	target, err := ReadLinkAt(parent, "mylink")
	if err != nil {
		t.Fatalf("ReadLinkAt failed: %v", err)
	}
	if target != "/some/target/path" {
		t.Fatalf("expected '/some/target/path', got %q", target)
	}
}

func TestRemoveChildren(t *testing.T) {
	tmpDir := t.TempDir()

	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../file1.txt", filepath.Join(subDir, "link")); err != nil {
		t.Fatal(err)
	}

	d, err := os.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if err := RemoveChildren(d); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestRemoveChildren_FirstError(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "locked"), nil, 0o000); err != nil {
		t.Fatal(err)
	}

	d, _ := os.Open(tmpDir)
	defer d.Close()

	err := RemoveChildren(d)
	if err != nil {
		if _, ok := err.(*os.PathError); !ok {
			t.Errorf("expected *os.PathError, got %T", err)
		}
	}
}

// --- CopyFolderContents tests ---

func TestCopyFolderContents_BasicTree(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	createTestTree(t, srcDir)

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	// Regular files must be present with correct contents.
	for file, want := range map[string]string{
		"file1.txt":                   "file1",
		"subdir1/file2.txt":           "file2",
		"subdir1/subsubdir/file3.txt": "file3",
	} {
		got := readFileInDir(t, dstDir, file)
		if got != want {
			t.Errorf("%s: expected %q, got %q", file, want, got)
		}
	}

	// Subdirectories must be present.
	for _, dir := range []string{"subdir1", "subdir2", "subdir1/subsubdir"} {
		info, err := os.Stat(filepath.Join(dstDir, dir))
		if err != nil || !info.IsDir() {
			t.Errorf("directory %s not present in dst", dir)
		}
	}

	// Without Follow_symlinks, symlinks must be copied verbatim.
	for linkName, wantTarget := range map[string]string{
		"link_to_file1":                       "file1.txt",
		"subdir2/link_to_subdir1":             "../subdir1",
		"subdir1/subsubdir/link_to_root_file": "../../file1.txt",
	} {
		info, err := os.Lstat(filepath.Join(dstDir, linkName))
		if err != nil {
			t.Errorf("%s: not found in dst: %v", linkName, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: expected symlink, got %v", linkName, info.Mode())
			continue
		}
		gotTarget, _ := os.Readlink(filepath.Join(dstDir, linkName))
		if gotTarget != wantTarget {
			t.Errorf("%s: expected target %q, got %q", linkName, wantTarget, gotTarget)
		}
	}
}

func TestCopyFolderContents_FileContents(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	testFiles := map[string][]byte{
		"empty.txt":  {},
		"small.txt":  []byte("hello world"),
		"binary.bin": {0x00, 0x01, 0x02, 0xFF, 0xFE},
	}
	for name, data := range testFiles {
		if err := os.WriteFile(filepath.Join(srcDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{Disallow_hardlinks: true}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	for name, want := range testFiles {
		got, err := os.ReadFile(filepath.Join(dstDir, name))
		if err != nil {
			t.Errorf("reading %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: content mismatch (len %d vs %d)", name, len(got), len(want))
		}
	}
}

func TestCopyFolderContents_WithHardlinks(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	srcInfo, _ := os.Stat(filepath.Join(srcDir, "file.txt"))
	dstInfo, _ := os.Stat(filepath.Join(dstDir, "file.txt"))
	if srcInfo.Sys().(*syscall.Stat_t).Ino != dstInfo.Sys().(*syscall.Stat_t).Ino {
		t.Error("expected src and dst files to share an inode (hardlinked)")
	}
}

func TestCopyFolderContents_NoHardlinks(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	content := []byte("file content")
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{Disallow_hardlinks: true}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	srcInfo, _ := os.Stat(filepath.Join(srcDir, "file.txt"))
	dstInfo, _ := os.Stat(filepath.Join(dstDir, "file.txt"))
	if srcInfo.Sys().(*unix.Stat_t).Ino == dstInfo.Sys().(*unix.Stat_t).Ino {
		t.Error("expected different inodes when hardlinks are disabled")
	}

	got, _ := os.ReadFile(filepath.Join(dstDir, "file.txt"))
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch: expected %q, got %q", content, got)
	}
}

func TestCopyFolderContents_SymlinksWithoutFollow(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(srcDir, "rel_link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/absolute/path", filepath.Join(srcDir, "abs_link")); err != nil {
		t.Fatal(err)
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{Disallow_hardlinks: true}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	for linkName, wantTarget := range map[string]string{
		"rel_link": "file.txt",
		"abs_link": "/absolute/path",
	} {
		info, err := os.Lstat(filepath.Join(dstDir, linkName))
		if err != nil {
			t.Errorf("%s: not found in dst: %v", linkName, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: expected a symlink", linkName)
			continue
		}
		gotTarget, _ := os.Readlink(filepath.Join(dstDir, linkName))
		if gotTarget != wantTarget {
			t.Errorf("%s: expected target %q, got %q", linkName, wantTarget, gotTarget)
		}
	}
}

func TestCopyFolderContents_FollowSymlinks_RegularFile(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "real.txt"), []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(srcDir, "link_to_real")); err != nil {
		t.Fatal(err)
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
		Follow_symlinks:    true,
	}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	// real.txt must be present with correct content.
	if got := readFileInDir(t, dstDir, "real.txt"); got != "real content" {
		t.Errorf("real.txt: expected 'real content', got %q", got)
	}

	// link_to_real must be accessible and yield the correct content when followed.
	data, err := os.ReadFile(filepath.Join(dstDir, "link_to_real"))
	if err != nil {
		t.Fatalf("link_to_real not accessible in dst: %v", err)
	}
	if string(data) != "real content" {
		t.Errorf("link_to_real: expected 'real content', got %q", string(data))
	}

	// If link_to_real is a symlink its target must not point into srcDir.
	info, _ := os.Lstat(filepath.Join(dstDir, "link_to_real"))
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(filepath.Join(dstDir, "link_to_real"))
		if filepath.IsAbs(target) && filepath.HasPrefix(target, srcDir) {
			t.Errorf("link_to_real points into srcDir: %q", target)
		}
	}
}

func TestCopyFolderContents_FollowSymlinks_Directory(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	if err := os.Mkdir(filepath.Join(srcDir, "real_dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "real_dir", "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real_dir", filepath.Join(srcDir, "link_to_dir")); err != nil {
		t.Fatal(err)
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
		Follow_symlinks:    true,
	}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	// real_dir must be copied as a real directory with its content.
	info, err := os.Stat(filepath.Join(dstDir, "real_dir"))
	if err != nil || !info.IsDir() {
		t.Error("real_dir should be a directory in dst")
	}
	if got := readFileInDir(t, dstDir, "real_dir/file.txt"); got != "content" {
		t.Errorf("real_dir/file.txt: expected 'content', got %q", got)
	}

	// link_to_dir must resolve to something accessible inside dstDir.
	if _, err := os.Stat(filepath.Join(dstDir, "link_to_dir")); err != nil {
		t.Fatalf("link_to_dir not accessible in dst: %v", err)
	}
}

func TestCopyFolderContents_SymlinkLoop(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// a -> b -> a forms a loop.
	if err := os.Symlink("b", filepath.Join(srcDir, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(srcDir, "b")); err != nil {
		t.Fatal(err)
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	// With Follow_symlinks=true the loop must be detected via EvalSymlinks failing
	// and the symlinks copied verbatim without returning an error.
	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
		Follow_symlinks:    true,
	}); err != nil {
		t.Fatalf("CopyFolderContents with symlink loop returned error: %v", err)
	}

	for linkName, wantTarget := range map[string]string{"a": "b", "b": "a"} {
		target, err := os.Readlink(filepath.Join(dstDir, linkName))
		if err != nil {
			t.Errorf("%s: expected a symlink in dst: %v", linkName, err)
			continue
		}
		if target != wantTarget {
			t.Errorf("%s: expected target %q, got %q", linkName, wantTarget, target)
		}
	}
}

func TestCopyFolderContents_DeepTree_NoHardlinks(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	createTestTree(t, srcDir)

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
	}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	// All regular files must be present with correct contents and distinct inodes.
	for file, want := range map[string]string{
		"file1.txt":                   "file1",
		"subdir1/file2.txt":           "file2",
		"subdir1/subsubdir/file3.txt": "file3",
	} {
		got := readFileInDir(t, dstDir, file)
		if got != want {
			t.Errorf("%s: expected %q, got %q", file, want, got)
		}
		srcStat, _ := os.Stat(filepath.Join(srcDir, file))
		dstStat, _ := os.Stat(filepath.Join(dstDir, file))
		if srcStat.Sys().(*unix.Stat_t).Ino == dstStat.Sys().(*unix.Stat_t).Ino {
			t.Errorf("%s: expected different inodes when hardlinks are disabled", file)
		}
	}

	// Symlinks must be copied verbatim.
	for linkName, wantTarget := range map[string]string{
		"link_to_file1":                       "file1.txt",
		"subdir2/link_to_subdir1":             "../subdir1",
		"subdir1/subsubdir/link_to_root_file": "../../file1.txt",
	} {
		info, err := os.Lstat(filepath.Join(dstDir, linkName))
		if err != nil {
			t.Errorf("%s: not found in dst: %v", linkName, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: expected symlink", linkName)
			continue
		}
		gotTarget, _ := os.Readlink(filepath.Join(dstDir, linkName))
		if gotTarget != wantTarget {
			t.Errorf("%s: expected target %q, got %q", linkName, wantTarget, gotTarget)
		}
	}
}

func TestCopyFolderContents_FilterFiles(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	for _, name := range []string{"keep.txt", "skip.txt"} {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	src := openTestDir(t, srcDir)
	defer src.Close()
	dst := openTestDir(t, dstDir)
	defer dst.Close()

	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
		Filter_files:       func(_ *os.File, info os.FileInfo) bool { return info.Name() != "skip.txt" },
	}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dstDir, "keep.txt")); err != nil {
		t.Error("keep.txt should be present")
	}
	if _, err := os.Stat(filepath.Join(dstDir, "skip.txt")); err == nil {
		t.Error("skip.txt should have been filtered out")
	}
}

// --- Linux file-descriptor leak tests ---

func TestCopyFolderContents_FDLeaks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd leak test only runs on Linux (/proc/self/fd)")
	}
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	createTestTree(t, srcDir)

	src := openTestDir(t, srcDir)
	dst := openTestDir(t, dstDir)

	fdsBefore := countOpenFDs(t)
	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}
	fdsAfter := countOpenFDs(t)
	src.Close()
	dst.Close()

	if fdsAfter != fdsBefore {
		t.Errorf("fd leak: before=%d after=%d (leaked %d)", fdsBefore, fdsAfter, fdsAfter-fdsBefore)
	}
}

func TestCopyFolderContents_FDLeaks_NoHardlinks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd leak test only runs on Linux (/proc/self/fd)")
	}
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	createTestTree(t, srcDir)

	src := openTestDir(t, srcDir)
	dst := openTestDir(t, dstDir)

	fdsBefore := countOpenFDs(t)
	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
	}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}
	fdsAfter := countOpenFDs(t)
	src.Close()
	dst.Close()

	if fdsAfter != fdsBefore {
		t.Errorf("fd leak: before=%d after=%d (leaked %d)", fdsBefore, fdsAfter, fdsAfter-fdsBefore)
	}
}

func TestCopyFolderContents_FDLeaks_FollowSymlinks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd leak test only runs on Linux (/proc/self/fd)")
	}
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	createTestTree(t, srcDir)

	src := openTestDir(t, srcDir)
	dst := openTestDir(t, dstDir)

	fdsBefore := countOpenFDs(t)
	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
		Follow_symlinks:    true,
	}); err != nil {
		t.Fatalf("CopyFolderContents failed: %v", err)
	}
	fdsAfter := countOpenFDs(t)
	src.Close()
	dst.Close()

	if fdsAfter != fdsBefore {
		t.Errorf("fd leak: before=%d after=%d (leaked %d)", fdsBefore, fdsAfter, fdsAfter-fdsBefore)
	}
}

func TestCopyFolderContents_FDLeaks_SymlinkLoop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd leak test only runs on Linux (/proc/self/fd)")
	}
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	if err := os.Symlink("b", filepath.Join(srcDir, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(srcDir, "b")); err != nil {
		t.Fatal(err)
	}

	src := openTestDir(t, srcDir)
	dst := openTestDir(t, dstDir)

	fdsBefore := countOpenFDs(t)
	if err := CopyFolderContents(context.Background(), src, dst, CopyFolderOptions{
		Disallow_hardlinks: true,
		Follow_symlinks:    true,
	}); err != nil {
		t.Fatalf("CopyFolderContents with symlink loop failed: %v", err)
	}
	fdsAfter := countOpenFDs(t)
	src.Close()
	dst.Close()

	if fdsAfter != fdsBefore {
		t.Errorf("fd leak: before=%d after=%d (leaked %d)", fdsBefore, fdsAfter, fdsAfter-fdsBefore)
	}
}
