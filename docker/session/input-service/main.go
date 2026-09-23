package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const commandTimeout = 2 * time.Second

type inputMessage struct {
	Type   string  `json:"type"`
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Button int     `json:"button"`
	DeltaX float64 `json:"deltaX"`
	DeltaY float64 `json:"deltaY"`
	Key    string  `json:"key"`
	Code   string  `json:"code"`
	Text   string  `json:"text"`
}

type clipboardMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type inputDriver struct {
	display      string
	mu           sync.Mutex
	clipboardMu  sync.Mutex
	clipboardCmd *exec.Cmd
	fullscreenMu sync.Mutex
	fullscreen   bool
	fullscreenAt time.Time
	dialogMu     sync.Mutex
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func newInputDriver(display string) *inputDriver {
	return &inputDriver{display: display}
}

func (d *inputDriver) xdotool(args ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runXdotool(args...)
}

func (d *inputDriver) runXdotool(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// #nosec G204 -- xdotool is fixed and arguments are passed directly without a shell.
	cmd := exec.CommandContext(ctx, "xdotool", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+d.display)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
	}
	return nil
}

// typeText uses xdotool for ordinary text and clipboard paste for Unicode.
// XTEST key events cannot reliably represent CJK characters, while Chromium
// accepts UTF-8 text through the X clipboard just like a local paste.
func (d *inputDriver) typeText(text string) error {
	if isASCII(text) {
		return d.xdotool("type", "--clearmodifiers", "--", text)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.setClipboard(text); err != nil {
		return err
	}
	// Give xclip a moment to claim the X selection before Chromium receives
	// Ctrl+V. The clipboard owner remains alive until it is replaced.
	time.Sleep(10 * time.Millisecond)
	return d.runXdotool("key", "--clearmodifiers", "ctrl+v")
}

// setClipboard starts a managed xclip selection owner and returns immediately.
// xclip intentionally stays alive while it owns the clipboard; waiting for it
// here makes Unicode input block until the command timeout expires.
func (d *inputDriver) setClipboard(text string) error {
	d.clipboardMu.Lock()
	defer d.clipboardMu.Unlock()
	if d.clipboardCmd != nil && d.clipboardCmd.Process != nil {
		_ = d.clipboardCmd.Process.Kill()
		d.clipboardCmd = nil
	}

	cmd := exec.Command("xclip", "-selection", "clipboard")
	cmd.Env = append(os.Environ(), "DISPLAY="+d.display)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Start(); err != nil {
		return err
	}
	d.clipboardCmd = cmd
	go func() {
		_ = cmd.Wait()
		d.clipboardMu.Lock()
		if d.clipboardCmd == cmd {
			d.clipboardCmd = nil
		}
		d.clipboardMu.Unlock()
	}()
	return nil
}

func isASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] > 0x7f {
			return false
		}
	}
	return true
}

func main() {
	display := env("DISPLAY", ":1")
	port := env("RB_INPUT_PORT", "6084")
	offsetX := envInt("RB_INPUT_OFFSET_X", 0)
	offsetY := envInt("RB_INPUT_OFFSET_Y", envInt("RB_CHROME_WINDOW_TOP", 0))
	driver := newInputDriver(display)
	mouseMoves := newMouseMoveAccumulator(driver, 16*time.Millisecond)
	wheels := newWheelAccumulator(driver, time.Duration(envInt("RB_INPUT_WHEEL_INTERVAL_MS", 120))*time.Millisecond, envInt("RB_INPUT_WHEEL_MAX_STEPS", 1))
	go mouseMoves.run()
	go wheels.run()

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	http.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost {
			var state struct {
				Fullscreen bool `json:"fullscreen"`
			}
			if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			driver.fullscreenMu.Lock()
			driver.fullscreen = state.Fullscreen
			driver.fullscreenAt = time.Now()
			driver.fullscreenMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		driver.fullscreenMu.Lock()
		reported := driver.fullscreen && time.Since(driver.fullscreenAt) < 3*time.Second
		driver.fullscreenMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{
			"fullscreen": reported || activeWindowFullscreen(display),
		})
	})
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade input ws: %v", err)
			return
		}
		defer conn.Close()
		var writeMu sync.Mutex
		writeClipboard := func(reply *clipboardMessage) {
			writeMu.Lock()
			defer writeMu.Unlock()
			if err := conn.WriteJSON(reply); err != nil {
				log.Printf("write input ws: %v", err)
			}
		}

		for {
			var msg inputMessage
			if err := conn.ReadJSON(&msg); err != nil {
				if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					log.Printf("read input ws: %v", err)
				}
				return
			}
			if reply, err := handleMessage(driver, offsetX, offsetY, mouseMoves, wheels, writeClipboard, msg); err != nil {
				log.Printf("handle %s: %v", msg.Type, err)
			} else if reply != nil {
				writeClipboard(reply)
			}
		}
	})

	srv := &http.Server{
		Addr:              ":" + port,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go watchFileDialog(driver.display, envInt("RB_SCREEN_WIDTH", 1280), envInt("RB_SCREEN_HEIGHT", 752), driver)
	log.Printf("input service listening on :%s display=%s offset=%d,%d", port, display, offsetX, offsetY)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type fileDialogWindow struct {
	x, y, width, height int
}

// watchFileDialog keeps Chromium's native GTK chooser inside the VNC screen.
// Chromium can request a dialog taller than the virtual screen, hiding the
// Open/Cancel buttons and making an otherwise valid selection impossible.
func watchFileDialog(display string, screenWidth, screenHeight int, driver *inputDriver) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		driver.dialogMu.Lock()
		_, _ = normalizeFileDialog(display, screenWidth, screenHeight)
		driver.dialogMu.Unlock()
	}
}

func normalizeFileDialog(display string, screenWidth, screenHeight int) (fileDialogWindow, bool) {
	windowID, err := xdotoolOutput(display, "getactivewindow")
	if err != nil || windowID == "" {
		return fileDialogWindow{}, false
	}
	title, err := xdotoolOutput(display, "getwindowname", windowID)
	if err != nil || !isFileDialogTitle(title) {
		return fileDialogWindow{}, false
	}
	geometry, err := xdotoolOutput(display, "getwindowgeometry", "--shell", windowID)
	if err != nil {
		return fileDialogWindow{}, false
	}
	window := parseDialogGeometry(windowID, geometry)
	if window.width <= 0 || window.height <= 0 {
		return fileDialogWindow{}, false
	}
	maxWidth := screenWidth - 40
	maxHeight := screenHeight - 40
	if maxWidth < 640 {
		maxWidth = screenWidth
	}
	if maxHeight < 480 {
		maxHeight = screenHeight
	}
	targetWidth := window.width
	if targetWidth > 1100 || targetWidth > maxWidth {
		targetWidth = minInt(1100, maxWidth)
	}
	targetHeight := window.height
	if targetHeight > 700 || targetHeight > maxHeight {
		targetHeight = minInt(700, maxHeight)
	}
	targetX := (screenWidth - targetWidth) / 2
	targetY := (screenHeight - targetHeight) / 2
	if targetX < 0 {
		targetX = 0
	}
	if targetY < 0 {
		targetY = 0
	}
	// Only correct oversized/off-screen dialogs. Once the size is valid, leave
	// the user's position alone; window-manager frame coordinates differ from
	// the client coordinates passed to windowmove and would otherwise cause a
	// move on every watcher tick.
	if targetWidth != window.width || targetHeight != window.height || window.x < 0 || window.y < 0 {
		_ = xdotool(display, "windowsize", windowID, strconv.Itoa(targetWidth), strconv.Itoa(targetHeight))
		_ = xdotool(display, "windowmove", windowID, strconv.Itoa(targetX), strconv.Itoa(targetY))
		// Fluxbox reports frame coordinates (including decorations), which can
		// differ from the coordinates passed to windowmove. Re-read them so a
		// subsequent button click lands on the GTK client area.
		if updated, err := xdotoolOutput(display, "getwindowgeometry", "--shell", windowID); err == nil {
			window = parseDialogGeometry(windowID, updated)
		} else {
			window.x, window.y, window.width, window.height = targetX, targetY, targetWidth, targetHeight
		}
	}
	return window, true
}

func isFileDialogTitle(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	return strings.Contains(title, "open file") || strings.Contains(title, "open files") || strings.Contains(title, "file chooser")
}

func parseDialogGeometry(_ string, output string) fileDialogWindow {
	window := fileDialogWindow{}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			continue
		}
		switch parts[0] {
		case "X":
			window.x = value
		case "Y":
			window.y = value
		case "WIDTH":
			window.width = value
		case "HEIGHT":
			window.height = value
		}
	}
	return window
}

func xdotoolOutput(display string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// #nosec G204 -- xdotool is fixed and arguments are passed directly without a shell.
	cmd := exec.CommandContext(ctx, "xdotool", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func xdotool(display string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// #nosec G204 -- xdotool is fixed and arguments are passed directly without a shell.
	cmd := exec.CommandContext(ctx, "xdotool", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func handleMessage(driver *inputDriver, offsetX int, offsetY int, mouseMoves *mouseMoveAccumulator, wheels *wheelAccumulator, writeClipboard func(*clipboardMessage), msg inputMessage) (*clipboardMessage, error) {
	switch msg.Type {
	case "mouseMove":
		mouseMoves.add(msg.X+offsetX, msg.Y+offsetY)
		return nil, nil
	case "mouseDown":
		if err := mouseMoves.flush(); err != nil {
			return nil, err
		}
		return nil, driver.xdotool("mousedown", strconv.Itoa(mouseButton(msg.Button)))
	case "mouseUp":
		if err := mouseMoves.flush(); err != nil {
			return nil, err
		}
		return nil, driver.xdotool("mouseup", strconv.Itoa(mouseButton(msg.Button)))
	case "wheel":
		wheels.add(msg.DeltaX, msg.DeltaY)
		return nil, nil
	case "keyDown":
		key := xdoKey(msg)
		if key == "" {
			return nil, nil
		}
		return nil, driver.xdotool("keydown", key)
	case "keyUp":
		key := xdoKey(msg)
		if key == "" {
			return nil, nil
		}
		return nil, driver.xdotool("keyup", key)
	case "text":
		if msg.Text == "" {
			return nil, nil
		}
		return nil, driver.typeText(msg.Text)
	case "clipboardSet":
		driver.mu.Lock()
		err := driver.setClipboard(msg.Text)
		driver.mu.Unlock()
		if err != nil {
			log.Printf("handle clipboardSet: %v", err)
		}
		return nil, nil
	case "clipboardGet":
		go func() {
			text, err := getClipboard(driver.display)
			if err != nil {
				log.Printf("handle clipboardGet: %v", err)
				return
			}
			writeClipboard(&clipboardMessage{Type: "clipboardValue", Text: text})
		}()
		return nil, nil
	default:
		return nil, nil
	}
}

type mouseMoveAccumulator struct {
	driver   *inputDriver
	interval time.Duration
	mu       sync.Mutex
	x        int
	y        int
	pending  bool
}

func newMouseMoveAccumulator(driver *inputDriver, interval time.Duration) *mouseMoveAccumulator {
	return &mouseMoveAccumulator{
		driver:   driver,
		interval: interval,
	}
}

func (m *mouseMoveAccumulator) add(x int, y int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.x = x
	m.y = y
	m.pending = true
}

func (m *mouseMoveAccumulator) take() (int, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.x
	y := m.y
	pending := m.pending
	m.pending = false
	return x, y, pending
}

func (m *mouseMoveAccumulator) flush() error {
	x, y, pending := m.take()
	if !pending {
		return nil
	}
	return m.driver.xdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y))
}

func (m *mouseMoveAccumulator) run() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := m.flush(); err != nil {
			log.Printf("handle mouseMove: %v", err)
		}
	}
}

type wheelAccumulator struct {
	driver   *inputDriver
	interval time.Duration
	maxSteps int
	mu       sync.Mutex
	deltaX   float64
	deltaY   float64
}

func newWheelAccumulator(driver *inputDriver, interval time.Duration, maxSteps int) *wheelAccumulator {
	if maxSteps < 1 {
		maxSteps = 1
	}
	return &wheelAccumulator{
		driver:   driver,
		interval: interval,
		maxSteps: maxSteps,
	}
}

func (w *wheelAccumulator) add(deltaX float64, deltaY float64) {
	if abs(deltaX) < 1 && abs(deltaY) < 1 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if abs(deltaY) >= abs(deltaX) {
		if oppositeSign(w.deltaY, deltaY) {
			w.deltaY = deltaY
		} else {
			w.deltaY += deltaY
		}
		w.deltaX = 0
		return
	}

	if oppositeSign(w.deltaX, deltaX) {
		w.deltaX = deltaX
	} else {
		w.deltaX += deltaX
	}
	w.deltaY = 0
}

func (w *wheelAccumulator) take() (float64, float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	deltaX := w.deltaX
	deltaY := w.deltaY
	w.deltaX = 0
	w.deltaY = 0
	return deltaX, deltaY
}

func (w *wheelAccumulator) run() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for range ticker.C {
		deltaX, deltaY := w.take()
		if err := wheel(w.driver, deltaX, deltaY, w.maxSteps); err != nil {
			log.Printf("handle wheel: %v", err)
		}
	}
}

func wheel(driver *inputDriver, deltaX float64, deltaY float64, maxSteps int) error {
	if abs(deltaY) >= abs(deltaX) {
		if abs(deltaY) < 1 {
			return nil
		}
		button := "5"
		if deltaY < 0 {
			button = "4"
		}
		return driver.xdotool("click", "--repeat", strconv.Itoa(wheelSteps(deltaY, maxSteps)), "--delay", "0", button)
	}
	if abs(deltaX) < 1 {
		return nil
	}
	button := "7"
	if deltaX < 0 {
		button = "6"
	}
	return driver.xdotool("click", "--repeat", strconv.Itoa(wheelSteps(deltaX, maxSteps)), "--delay", "0", button)
}

func wheelSteps(delta float64, maxSteps int) int {
	if maxSteps < 1 {
		maxSteps = 1
	}
	steps := int(abs(delta) / 120)
	if steps < 1 {
		steps = 1
	}
	if steps > maxSteps {
		steps = maxSteps
	}
	return steps
}

func getClipboard(display string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-o")
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

func activeWindowFullscreen(display string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	getWindow := exec.CommandContext(ctx, "xdotool", "getactivewindow")
	getWindow.Env = append(os.Environ(), "DISPLAY="+display)
	windowID, err := getWindow.Output()
	if err != nil {
		return false
	}
	window, err := strconv.ParseUint(strings.TrimSpace(string(windowID)), 10, 64)
	if err != nil || window == 0 {
		return false
	}
	// #nosec G204 -- xprop is fixed and the window identifier is parsed as an unsigned integer.
	state := exec.CommandContext(ctx, "xprop", "-id", strconv.FormatUint(window, 10), "_NET_WM_STATE")
	state.Env = append(os.Environ(), "DISPLAY="+display)
	out, err := state.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "_NET_WM_STATE_FULLSCREEN")
}

func mouseButton(button int) int {
	switch button {
	case 1:
		return 2
	case 2:
		return 3
	default:
		return 1
	}
}

func xdoKey(msg inputMessage) string {
	if mapped, ok := keyMap[msg.Key]; ok {
		return mapped
	}
	if mapped, ok := codeMap[msg.Code]; ok {
		return mapped
	}
	if len(msg.Key) == 1 {
		return strings.ToLower(msg.Key)
	}
	return ""
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func oppositeSign(a float64, b float64) bool {
	return (a < 0 && b > 0) || (a > 0 && b < 0)
}

var keyMap = map[string]string{
	"Enter":      "Return",
	"Backspace":  "BackSpace",
	"Tab":        "Tab",
	"Escape":     "Escape",
	"Delete":     "Delete",
	"Home":       "Home",
	"End":        "End",
	"PageUp":     "Page_Up",
	"PageDown":   "Page_Down",
	"ArrowLeft":  "Left",
	"ArrowUp":    "Up",
	"ArrowRight": "Right",
	"ArrowDown":  "Down",
	" ":          "space",
	"Shift":      "Shift_L",
	"Control":    "Control_L",
	"Alt":        "Alt_L",
	"Meta":       "Super_L",
}

var codeMap = map[string]string{
	"Space":        "space",
	"Minus":        "minus",
	"Equal":        "equal",
	"BracketLeft":  "bracketleft",
	"BracketRight": "bracketright",
	"Backslash":    "backslash",
	"Semicolon":    "semicolon",
	"Quote":        "apostrophe",
	"Comma":        "comma",
	"Period":       "period",
	"Slash":        "slash",
	"Backquote":    "grave",
}

func init() {
	for ch := 'A'; ch <= 'Z'; ch++ {
		codeMap["Key"+string(ch)] = strings.ToLower(string(ch))
	}
	for ch := '0'; ch <= '9'; ch++ {
		codeMap["Digit"+string(ch)] = string(ch)
	}
}
