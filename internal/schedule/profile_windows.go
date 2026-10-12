//go:build windows

package schedule

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type savedLogin struct {
	exe      string
	userData string
	profile  string
}

func findLogin(rawURL string) (savedLogin, error) {
	needles := hostNeedles(rawURL)
	var readErr error
	for _, browser := range installedBrowsers() {
		for _, profile := range browserProfiles(browser.userData) {
			data, err := readCookieDB(browser.userData, profile)
			if err != nil {
				readErr = err
				continue
			}
			if len(data) == 0 || !containsHost(data, needles) {
				continue
			}
			browser.profile = profile
			return browser, nil
		}
	}
	if readErr != nil {
		return savedLogin{}, fmt.Errorf("没能读取浏览器里的登录状态: %w", readErr)
	}
	return savedLogin{}, nil
}

func seedLogin(dst string, login savedLogin) error {
	if err := copyLocked(filepath.Join(login.userData, "Local State"), filepath.Join(dst, "Local State")); err != nil {
		return fmt.Errorf("没能读取浏览器登录状态: %w", err)
	}
	src, kind := cookiePath(login.userData, login.profile)
	if src == "" {
		return fmt.Errorf("浏览器里没有 Cookie")
	}
	var dstCookies string
	if kind == "network" {
		dstCookies = filepath.Join(dst, "Default", "Network", "Cookies")
	} else {
		dstCookies = filepath.Join(dst, "Default", "Cookies")
	}
	if err := copyLocked(src, dstCookies); err != nil {
		return fmt.Errorf("没能读取浏览器登录状态: %w", err)
	}
	_ = copyLocked(src+"-wal", dstCookies+"-wal")
	_ = copyLocked(src+"-shm", dstCookies+"-shm")
	return nil
}

func installedBrowsers() []savedLogin {
	local := os.Getenv("LocalAppData")
	pf := os.Getenv("ProgramFiles")
	pf86 := os.Getenv("ProgramFiles(x86)")
	candidates := []savedLogin{
		{exe: firstExisting(
			filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
		), userData: filepath.Join(local, `Google\Chrome\User Data`)},
		{exe: firstExisting(
			filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		), userData: filepath.Join(local, `Microsoft\Edge\User Data`)},
	}
	var out []savedLogin
	for _, item := range candidates {
		if item.exe != "" && dirExists(item.userData) {
			out = append(out, item)
		}
	}
	return out
}

func browserProfiles(userData string) []string {
	entries, err := os.ReadDir(userData)
	if err != nil {
		return []string{"Default"}
	}
	var profiles []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() && (name == "Default" || strings.HasPrefix(name, "Profile ")) {
			profiles = append(profiles, name)
		}
	}
	if len(profiles) == 0 {
		return []string{"Default"}
	}
	return profiles
}

func cookiePath(userData, profile string) (string, string) {
	network := filepath.Join(userData, profile, "Network", "Cookies")
	if fileExists(network) {
		return network, "network"
	}
	old := filepath.Join(userData, profile, "Cookies")
	if fileExists(old) {
		return old, "old"
	}
	return "", ""
}

func readCookieDB(userData, profile string) ([]byte, error) {
	path, _ := cookiePath(userData, profile)
	if path == "" {
		return nil, nil
	}
	data, err := readLocked(path)
	if err != nil {
		return nil, err
	}
	wal, walErr := readLocked(path + "-wal")
	if walErr == nil && len(wal) > 0 {
		data = append(data, wal...)
	}
	return data, nil
}

func hostNeedles(rawURL string) []string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host == "" {
		return nil
	}
	if net.ParseIP(host) != nil {
		return []string{host}
	}
	needles := []string{host}
	parts := strings.Split(host, ".")
	if len(parts) > 2 {
		needles = append(needles, strings.Join(parts[len(parts)-2:], "."))
	}
	return needles
}

func containsHost(data []byte, needles []string) bool {
	if len(needles) == 0 {
		return false
	}
	lower := bytes.ToLower(data)
	for _, needle := range needles {
		if needle != "" && bytes.Contains(lower, []byte(needle)) {
			return true
		}
	}
	return false
}

func copyLocked(src, dst string) error {
	data, err := readLocked(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func readLocked(path string) ([]byte, error) {
	if data, ok := cachedLocked(path); ok {
		return data, nil
	}
	data, err := readShared(path, false)
	if err == nil {
		storeLocked(path, data)
		return data, nil
	}
	first := err
	if privErr := enableBackupPrivilege(); privErr == nil {
		if data, err = readShared(path, true); err == nil {
			storeLocked(path, data)
			return data, nil
		}
	}
	data, err = readFromBrowser(path)
	if err == nil {
		storeLocked(path, data)
		return data, nil
	}
	return nil, first
}

var lockedFiles struct {
	sync.Mutex
	items map[string]lockedFile
}

type lockedFile struct {
	data []byte
	at   time.Time
}

func cachedLocked(path string) ([]byte, bool) {
	lockedFiles.Lock()
	defer lockedFiles.Unlock()
	item, ok := lockedFiles.items[path]
	if !ok || time.Since(item.at) > 2*time.Minute {
		return nil, false
	}
	out := make([]byte, len(item.data))
	copy(out, item.data)
	return out, true
}

func storeLocked(path string, data []byte) {
	lockedFiles.Lock()
	defer lockedFiles.Unlock()
	if lockedFiles.items == nil {
		lockedFiles.items = map[string]lockedFile{}
	}
	copied := make([]byte, len(data))
	copy(copied, data)
	lockedFiles.items[path] = lockedFile{data: copied, at: time.Now()}
}

func readShared(path string, backup bool) ([]byte, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if backup {
		flags = windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(ptr, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(handle)
	buf := make([]byte, 64*1024)
	var out []byte
	for {
		var n uint32
		readErr := windows.ReadFile(handle, buf, &n, nil)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if readErr != nil || n == 0 {
			if readErr == windows.ERROR_HANDLE_EOF || n == 0 {
				return out, nil
			}
			return nil, readErr
		}
	}
}

func enableBackupPrivilege() error {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token)
	if err != nil {
		return err
	}
	defer token.Close()
	name, err := windows.UTF16PtrFromString("SeBackupPrivilege")
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{{
			Luid:       luid,
			Attributes: windows.SE_PRIVILEGE_ENABLED,
		}},
	}
	if err := windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil); err != nil {
		return err
	}
	if err := windows.GetLastError(); err != nil && err != windows.ERROR_SUCCESS {
		return err
	}
	return nil
}

func readFromBrowser(path string) ([]byte, error) {
	target, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	pids := processIDs("chrome.exe", "msedge.exe")
	if len(pids) == 0 {
		return nil, fmt.Errorf("浏览器没有打开")
	}
	marker, err := os.CreateTemp("", "wxclaw-type-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(marker.Name())
	defer marker.Close()
	entries, err := systemHandles()
	if err != nil {
		return nil, err
	}
	fileType, ok := ownFileType(entries, marker)
	if !ok {
		return nil, fmt.Errorf("没有识别出文件句柄")
	}
	opened := map[uint32]windows.Handle{}
	scan := &handleScan{}
	defer scan.close(opened)
	deadline := time.Now().Add(8 * time.Second)
	for _, entry := range entries {
		if time.Now().After(deadline) {
			break
		}
		if entry.ObjectTypeIndex != fileType {
			continue
		}
		proc, ok := opened[uint32(entry.UniqueProcessID)]
		if !ok {
			if _, wanted := pids[uint32(entry.UniqueProcessID)]; !wanted {
				continue
			}
			proc, err = windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, uint32(entry.UniqueProcessID))
			if err != nil {
				opened[uint32(entry.UniqueProcessID)] = 0
				continue
			}
			opened[uint32(entry.UniqueProcessID)] = proc
		}
		if proc == 0 {
			continue
		}
		dup, ok := scan.duplicate(proc, windows.Handle(entry.HandleValue))
		if !ok || !isDiskFile(dup) {
			if ok {
				windows.CloseHandle(dup)
			}
			continue
		}
		name, ok := handlePath(dup)
		if !ok || !samePath(name, target) {
			windows.CloseHandle(dup)
			continue
		}
		data, readErr := mapHandle(dup)
		windows.CloseHandle(dup)
		if readErr != nil {
			return nil, readErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("没有在浏览器进程里找到登录文件")
}

type handleScan struct {
	wg   sync.WaitGroup
	mu   sync.Mutex
	busy map[windows.Handle]int
}

func (s *handleScan) close(opened map[uint32]windows.Handle) {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, handle := range opened {
		if handle != 0 && s.busy[handle] == 0 {
			windows.CloseHandle(handle)
		}
	}
}

func (s *handleScan) duplicate(proc, src windows.Handle) (windows.Handle, bool) {
	s.mu.Lock()
	if s.busy == nil {
		s.busy = map[windows.Handle]int{}
	}
	s.busy[proc]++
	s.mu.Unlock()
	s.wg.Add(1)
	type result struct {
		handle windows.Handle
		err    error
	}
	done := make(chan result, 1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.busy[proc]--
			if s.busy[proc] <= 0 {
				delete(s.busy, proc)
			}
			s.mu.Unlock()
		}()
		defer func() { recover() }()
		var dup windows.Handle
		err := windows.DuplicateHandle(proc, src, windows.CurrentProcess(), &dup, 0, false, windows.DUPLICATE_SAME_ACCESS)
		done <- result{dup, err}
	}()
	select {
	case item := <-done:
		return item.handle, item.err == nil && item.handle != 0
	case <-time.After(25 * time.Millisecond):
		go func() {
			item := <-done
			if item.err == nil && item.handle != 0 {
				windows.CloseHandle(item.handle)
			}
		}()
		return 0, false
	}
}

func isDiskFile(handle windows.Handle) bool {
	kind, _, _ := getFileType.Call(uintptr(handle))
	return kind == 1
}

var getFileType = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetFileType")

func ownFileType(entries []handleEntryEx, file *os.File) (uint16, bool) {
	pid := uint32(os.Getpid())
	handle := uintptr(file.Fd())
	for _, entry := range entries {
		if uint32(entry.UniqueProcessID) == pid && entry.HandleValue == handle {
			return entry.ObjectTypeIndex, true
		}
	}
	return 0, false
}

func processIDs(names ...string) map[uint32]struct{} {
	wanted := map[string]struct{}{}
	for _, name := range names {
		wanted[strings.ToLower(name)] = struct{}{}
	}
	out := map[uint32]struct{}{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return out
	}
	for {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if _, ok := wanted[name]; ok {
			out[entry.ProcessID] = struct{}{}
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return out
}

type handleEntryEx struct {
	Object           uintptr
	UniqueProcessID  uintptr
	HandleValue      uintptr
	GrantedAccess    uint32
	CreatorBackTrace uint16
	ObjectTypeIndex  uint16
	HandleAttributes uint32
	Reserved         uint32
}

func systemHandles() ([]handleEntryEx, error) {
	proc := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQuerySystemInformation")
	size := 1 << 20
	for i := 0; i < 8; i++ {
		buf := make([]byte, size)
		var needed uint32
		status, _, _ := proc.Call(64, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
		if status == 0xC0000004 {
			if needed > uint32(size) {
				size = int(needed) + 1<<16
			} else {
				size *= 2
			}
			continue
		}
		if status != 0 {
			return nil, fmt.Errorf("查询系统句柄失败")
		}
		count := *(*uintptr)(unsafe.Pointer(&buf[0]))
		step := int(unsafe.Sizeof(handleEntryEx{}))
		if step <= 0 || count > uintptr(len(buf)/step) {
			return nil, fmt.Errorf("查询系统句柄失败")
		}
		base := 16
		if base+int(count)*step > len(buf) {
			return nil, fmt.Errorf("查询系统句柄失败")
		}
		entries := make([]handleEntryEx, 0, count)
		for n := uintptr(0); n < count; n++ {
			entries = append(entries, *(*handleEntryEx)(unsafe.Add(unsafe.Pointer(&buf[0]), base+int(n)*step)))
		}
		return entries, nil
	}
	return nil, fmt.Errorf("查询系统句柄失败")
}

func handlePath(handle windows.Handle) (string, bool) {
	buf := make([]uint16, 1024)
	n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), 0)
	if err != nil || n == 0 || int(n) >= len(buf) {
		return "", false
	}
	return windows.UTF16ToString(buf[:n]), true
}

func samePath(a, b string) bool {
	a = strings.TrimPrefix(a, `\\?\`)
	b = strings.TrimPrefix(b, `\\?\`)
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func mapHandle(handle windows.Handle) ([]byte, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, err
	}
	size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
	if size <= 0 || size > 64<<20 {
		return nil, fmt.Errorf("登录文件大小异常")
	}
	mapping, err := windows.CreateFileMapping(handle, nil, windows.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(mapping)
	addr, err := windows.MapViewOfFile(mapping, windows.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	defer windows.UnmapViewOfFile(addr)
	out := make([]byte, size)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(addr)), size))
	return out, nil
}

func firstExisting(paths ...string) string {
	for _, path := range paths {
		if path != "" && fileExists(path) {
			return path
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
