package xnote

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

var teal = tcell.NewHexColor(0x7bd8c4)
var dim = tcell.NewHexColor(0xa4aebe)
var background = tcell.NewHexColor(0x151a22)
var surface = tcell.NewHexColor(0x202936)
var border = tcell.NewHexColor(0x394657)

type desktop struct {
	s                      *Store
	rows                   []Record
	locale                 string
	refreshRequests        chan struct{}
	loadedVersion          string
	libraryReadError       string
	layoutSignature        string
	ctx                    context.Context
	app                    *tview.Application
	pages                  *tview.Pages
	library                *tview.Table
	detail, header, notice *tview.TextView
	connectionView         *tview.TextView
	search                 *tview.InputField
	tabs                   *tview.Flex
	filterButtons          [3]*tview.Button
	showingDetail          bool
	viewportWidth          int
	viewportHeight         int
	body                   *tview.Flex
	actions                *actionBar
	footer                 *tview.Pages
	timeline               *timeline
	hits                   []Hit
	selected               string
	view                   string
	detailVersion          string
	signature              string
	modal                  bool
	modalContent           tview.Primitive
	noticeUntil            time.Time
	playingID              string
	playingTitle           string
	rate                   float64
}

func (d *desktop) t(k string) string { return tr(d.locale, k) }
func UI(ctx context.Context, s *Store) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer PlayerClose()
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	d := newDesktop(ctx, s)
	d.app.SetScreen(newFrameScreen(screen))
	d.refreshRequests = make(chan struct{}, 1)
	go d.loadLibrary(ctx)
	go func() {
		for ctx.Err() == nil {
			_ = s.IndexDurations(ctx)
			if !wait(ctx, 30*time.Second) {
				return
			}
		}
	}()
	engineCtx, engineCancel := context.WithCancel(ctx)
	engineDone := make(chan struct{})
	engineErrors := make(chan error, 1)
	go func() {
		defer close(engineDone)
		if e := Run(engineCtx, s); e != nil && !strings.Contains(e.Error(), "already running") {
			// The engine must finish even after the TUI stops processing draws.
			engineErrors <- e
		}
	}()
	defer func() {
		cancel()
		engineCancel()
		<-engineDone
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-engineErrors:
				d.app.QueueUpdateDraw(func() { d.message(e) })
			case <-ticker.C:
				d.requestLibraryRefresh()
			}
		}
	}()
	go func() { <-ctx.Done(); d.app.Stop() }()
	return d.app.Run()
}
func newDesktop(ctx context.Context, s *Store) *desktop {
	d := &desktop{s: s, ctx: ctx, app: tview.NewApplication(), pages: tview.NewPages(), rate: 1, locale: s.Config().Locale}
	tview.Styles.PrimitiveBackgroundColor = background
	tview.Styles.ContrastBackgroundColor = surface
	tview.Styles.BorderColor = border
	tview.Styles.PrimaryTextColor = tcell.NewHexColor(0xe4eaf2)
	tview.Styles.SecondaryTextColor = dim
	tview.Styles.ContrastSecondaryTextColor = teal
	// Focus uses color, not a second competing border weight.
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical
	tview.Borders.TopLeftFocus = tview.Borders.TopLeft
	tview.Borders.TopRightFocus = tview.Borders.TopRight
	tview.Borders.BottomLeftFocus = tview.Borders.BottomLeft
	tview.Borders.BottomRightFocus = tview.Borders.BottomRight
	tview.Styles.TitleColor = teal
	d.header = tview.NewTextView().SetDynamicColors(true)
	d.notice = tview.NewTextView().SetTextColor(dim).SetDynamicColors(true).SetWrap(false)
	d.library = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetSeparator(' ').SetEvaluateAllRows(false).
		SetSelectedStyle(tcell.StyleDefault.Background(teal).Foreground(background).Bold(true))
	d.library.SetBorderPadding(0, 0, 1, 1)
	d.detail = tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetWordWrap(true)
	d.detail.SetBorderPadding(0, 0, 1, 1)
	d.detail.SetHighlightedFunc(func(added, removed, remaining []string) {
		if len(added) > 0 {
			if seconds, e := strconv.ParseFloat(added[0], 64); e == nil {
				d.playAt(seconds)
			}
		}
	})
	d.search = tview.NewInputField().SetFieldBackgroundColor(tcell.NewHexColor(0x252d38)).SetChangedFunc(func(string) { d.showingDetail = false; d.refresh() })
	d.search.SetLabel(" / ").SetLabelColor(teal).SetPlaceholderTextColor(dim)
	d.search.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEscape {
			d.search.SetText("")
		}
		d.app.SetFocus(d.library)
	})
	d.tabs = tview.NewFlex()
	for i, key := range []string{"all", "done", "pending"} {
		key := key
		button := tview.NewButton("").SetStyle(tcell.StyleDefault.Background(teal).Foreground(background)).
			SetActivatedStyle(tcell.StyleDefault.Background(tcell.ColorWhite).Foreground(background).Bold(true).Underline(true))
		button.SetSelectedFunc(func() { d.setView(key) })
		d.filterButtons[i] = button
		d.tabs.AddItem(button, 0, 1, false)
		if i < 2 {
			d.tabs.AddItem(nil, 1, 0, false)
		}
	}
	d.body = tview.NewFlex().AddItem(d.library, 0, 1, true)
	d.timeline = &timeline{Box: tview.NewBox(), desktop: d}
	d.timeline.SetBackgroundColor(surface)
	d.actions = newActionBar()
	d.makeActions()
	d.footer = tview.NewPages().AddPage("actions", d.actions, true, true).AddPage("hint", d.notice, true, false)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.header, 3, 0, false).
		AddItem(d.tabs, 1, 0, false).AddItem(nil, 1, 0, false).
		AddItem(d.search, 1, 0, false).AddItem(nil, 1, 0, false).
		AddItem(d.body, 0, 1, true).
		AddItem(d.timeline, 3, 0, false).
		AddItem(d.footer, 1, 0, false)
	root.SetBorderPadding(1, 0, 1, 1)
	d.pages.AddPage("main", root, true, true)
	d.library.SetSelectionChangedFunc(d.selectRecording)
	d.library.SetSelectedFunc(func(int, int) { d.openRecording() })
	d.library.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftDoubleClick {
			row, col := d.library.CellAt(event.Position())
			if row > 0 && row <= len(d.hits) {
				d.library.Select(row, col)
				d.openRecording()
			}
			return action, nil
		}
		return action, event
	})
	d.app.SetInputCapture(d.handleKey)
	d.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		width, height := screen.Size()
		d.layout(width, height)
		return false
	})
	d.app.SetRoot(d.pages, true).EnableMouse(true).EnablePaste(true).SetFocus(d.library)
	d.notice.SetText(d.t("keyboard_hint"))
	d.view = "all"
	d.refresh()
	return d
}
func (d *desktop) makeActions() {
	d.actions.Clear()
	d.actions.AddButton("", d.primaryAction).
		AddButton("", d.togglePlayback).
		AddButton("", d.deleteSelected).
		AddButton("m "+d.t("more_short"), d.menu).
		AddButton("s "+d.t("settings"), d.settings).
		AddButton("? "+d.t("help"), d.help)
	d.updateActionLabels()
}
func (d *desktop) updateActionLabels() {
	label := "Enter " + d.t("open_short")
	if d.showingDetail {
		label = "Esc " + d.t("back_short")
	}
	d.actions.GetButton(0).SetLabel(label)
	playLabel := d.t("play_short")
	_, _, playing := PlayerPosition()
	if playing && d.playingID == d.selected {
		playLabel = d.t("pause")
	}
	d.actions.GetButton(1).SetLabel("Space " + playLabel)
	d.actions.GetButton(2).SetLabel("Del " + d.t("delete_short"))
	d.actions.GetButton(3).SetLabel("m " + d.t("more_short"))
	d.actions.GetButton(4).SetLabel("s " + d.t("settings"))
	d.actions.GetButton(5).SetLabel("? " + d.t("help"))
	if r, ok := d.record(); ok && r.Trashed {
		d.actions.GetButton(2).SetLabel("u " + d.t("restore_short"))
	}
}
func (d *desktop) actionButtons() []tview.Primitive {
	var buttons []tview.Primitive
	for i := 0; i < d.actions.GetButtonCount(); i++ {
		buttons = append(buttons, d.actions.GetButton(i))
	}
	return buttons
}
func (d *desktop) message(e error) {
	if e != nil {
		d.setNotice(e.Error())
	} else {
		d.setNotice(d.t("saved"))
	}
}
func (d *desktop) record() (Record, bool) {
	for _, h := range d.hits {
		if h.Record.ID == d.selected {
			return h.Record, true
		}
	}
	return Record{}, false
}

func (d *desktop) refresh() {
	if d.refreshRequests != nil {
		d.requestLibraryRefresh()
	} else {
		rows, err := d.s.Records()
		if err != nil {
			d.message(err)
			return
		}
		d.rows = rows
	}
	d.renderLibrary()
}

func (d *desktop) renderLibrary() {
	d.locale = d.s.Config().Locale
	hits := d.s.searchRecords(d.rows, d.search.GetText(), d.view == "trash")
	filtered := []Hit{}
	for _, h := range hits {
		r := h.Record
		if d.view == "done" && r.State != "done" || d.view == "pending" && (r.State == "done" || r.State == "no_speech") {
			continue
		}
		filtered = append(filtered, h)
	}
	d.hits = filtered
	if d.selected == "" && len(d.hits) > 0 {
		d.selected = d.hits[0].Record.ID
	}
	c := d.s.Config()
	st := d.s.Status()
	d.updateHeader(c, st)
	if d.modal && d.modalContent == d.connectionView && d.connectionView != nil {
		d.connectionView.SetText(d.connectionText())
	}
	d.updateTabs(hits)
	d.updateActionLabels()
	d.search.SetPlaceholder(d.t("search"))
	d.library.SetTitle(fmt.Sprintf(" %s · %d ", d.t("library"), len(d.hits)))
	signature := c.Locale + "/" + d.view + "/" + d.search.GetText()
	for _, h := range d.hits {
		signature += h.Record.ID + h.Record.UpdatedAt + h.Record.Title + h.Record.State + fmt.Sprint(h.Record.Duration)
	}
	if signature != d.signature {
		d.signature = signature
		selection, _ := d.library.GetSelection()
		previousSelection, previousID := selection, d.selected
		selection = max(1, min(selection, len(d.hits)))
		d.library.SetSelectionChangedFunc(nil).Clear()
		for col, label := range []string{d.t("recorded_at"), d.t("recording_name"), d.t("duration"), d.t("state")} {
			d.library.SetCell(0, col, tview.NewTableCell(label).SetTextColor(dim).SetAttributes(tcell.AttrUnderline).SetSelectable(false))
		}
		for i, h := range d.hits {
			r := h.Record
			color := dim
			if r.State == "done" {
				color = teal
			}
			if r.State == "error" || r.State == "download_error" {
				color = tcell.NewHexColor(0xefb879)
			}
			d.library.SetCell(i+1, 1, tview.NewTableCell(tview.Escape(d.recordingName(r))).SetExpansion(1).SetMaxWidth(70))
			d.library.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(displayDate(r.RecordedAt))).SetTextColor(dim))
			d.library.SetCell(i+1, 2, tview.NewTableCell(durationLabel(r)).SetAlign(tview.AlignRight).SetTextColor(dim))
			d.library.SetCell(i+1, 3, tview.NewTableCell(d.recordingState(r)).SetMaxWidth(18).SetTextColor(color))
			if r.ID == d.selected {
				selection = i + 1
			}
		}
		d.prepareLibraryColumns()
		if len(d.hits) > 0 {
			d.selected = d.hits[selection-1].Record.ID
			// A wheel scroll intentionally leaves the selected row offscreen.
			// Select, even with the same row, tells tview to jump back to it on
			// the next draw. A metadata refresh must not undo the user's scroll.
			if selection != previousSelection || d.selected != previousID {
				d.library.Select(selection, 0)
			}
		} else {
			d.selected = ""
		}
		d.library.SetSelectionChangedFunc(d.selectRecording)
	}
	if d.showingDetail || len(d.hits) == 0 {
		d.showDetail()
	}
}
func (d *desktop) showDetail() {
	r, ok := d.record()
	if !ok {
		d.detailVersion = ""
		d.detail.SetTitle(" " + d.t("transcript") + " ")
		key := "noresults"
		if d.search.GetText() == "" {
			key = "filter_empty"
			if d.view == "all" {
				key = "empty"
			}
		}
		d.detail.SetText("\n" + d.t(key))
		return
	}
	version := r.ID + r.UpdatedAt + d.locale + d.search.GetText()
	if version == d.detailVersion {
		return
	}
	changed := !strings.HasPrefix(d.detailVersion, r.ID)
	d.detailVersion = version
	d.detail.SetTitle(" " + d.t("transcript") + " · " + durationLabel(r) + " ")
	var b strings.Builder
	b.WriteString("[::b]" + tview.Escape(displayTitle(r)) + "[-:-:-]\n")
	metadata := d.recordingState(r) + "  ·  " + d.t("duration") + " " + durationLabel(r)
	if displayTitle(r) != displayDate(r.RecordedAt) {
		metadata = displayDate(r.RecordedAt) + "  ·  " + metadata
	}
	if r.Provider != "" {
		metadata += "  ·  " + r.Provider
	}
	b.WriteString("[#a4aebe]" + tview.Escape(metadata) + "[-]\n\n")
	if r.SummaryState != "" && r.SummaryState != "none" || r.Summary != nil {
		b.WriteString("[::b]" + d.t("summary_settings") + "[-:-:-]\n")
		if r.SummaryState != "" && r.SummaryState != "none" {
			b.WriteString("[#a4aebe]" + d.t("summary_"+r.SummaryState) + "[-]\n")
		}
		warning := r.SummaryError
		if warning == "" && r.Summary != nil {
			warning = r.Summary.Warning
		}
		if warning != "" {
			b.WriteString("[orange]" + tview.Escape(warning) + "[-]\n")
		}
		if r.Summary != nil {
			b.WriteString("[::b]" + tview.Escape(r.Summary.Title) + "[-:-:-]\n")
			if len(r.Summary.Keywords) > 0 {
				b.WriteString(d.t("summary_keywords") + ": " + tview.Escape(strings.Join(r.Summary.Keywords, " · ")) + "\n")
			}
			b.WriteString("\n" + tview.Escape(r.Summary.Markdown) + "\n")
		}
		b.WriteString("\n[::b]" + d.t("transcript") + "[-:-:-]\n")
	}

	if strings.TrimSpace(d.search.GetText()) != "" {
		for _, hit := range d.hits {
			if hit.Record.ID == r.ID {
				b.WriteString("[yellow]" + d.t("matches") + "[-]  " + tview.Escape(hit.Snippet) + "\n\n")
				break
			}
		}
	}
	if r.Error != "" {
		b.WriteString("[orange]" + tview.Escape(r.Error) + "[-]\n\n")
	}
	if len(r.Segments) > 0 {
		for _, segment := range r.Segments {
			prefix := clockTime(segment.Start)
			if segment.Timing == "chunk" {
				prefix = "≈ " + prefix
			}
			b.WriteString(fmt.Sprintf("[\"%.3f\"][#7bd8c4::b]%s[-:-:-][\"\"]  %s\n%s\n\n", segment.Start, prefix, tview.Escape(segment.Speaker), tview.Escape(segment.Text)))
		}
	} else if r.Transcript != "" {
		b.WriteString(tview.Escape(r.Transcript))
	} else if r.State == "no_speech" {
		b.WriteString(d.t("no_speech"))
	} else {
		b.WriteString(d.t("no_transcript"))
	}
	d.detail.SetText(b.String())
	if changed {
		d.detail.ScrollToBeginning()
	}
}
func (d *desktop) play() {
	r, ok := d.record()
	if !ok {
		return
	}
	_, duration, _ := PlayerPosition()
	if d.playingID == r.ID && duration > 0 {
		PlayerToggle()
		return
	}
	d.playAt(0)
}
func (d *desktop) playAt(seconds float64) {
	r, ok := d.record()
	if !ok {
		return
	}
	if r.Audio == "" {
		d.setNotice(d.t("download_first"))
		return
	}
	if d.playingID != r.ID {
		if e := PlayerOpen(r.Audio); e != nil {
			d.message(e)
			return
		}
		d.playingID = r.ID
		d.playingTitle = displayTitle(r)
		d.rate = 1
	}
	PlayerSeek(seconds)
	_, _, playing := PlayerPosition()
	if !playing {
		PlayerToggle()
	}
}
func (d *desktop) closeModal() {
	d.modal = false
	d.modalContent = nil
	d.pages.RemovePage("modal")
	if d.showingDetail {
		d.app.SetFocus(d.detail)
	} else {
		d.app.SetFocus(d.library)
	}
	d.refresh()
}
func (d *desktop) popup(p tview.Primitive, width, height int) {
	d.modal = true
	d.modalContent = p
	_, _, screenWidth, screenHeight := d.pages.GetRect()
	if screenWidth > 0 {
		width = min(width, max(1, screenWidth-2))
	}
	if screenHeight > 0 {
		height = min(height, max(1, screenHeight-2))
	}
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(nil, 0, 1, false).AddItem(tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(p, width, 1, true).AddItem(nil, 0, 1, false), height, 1, true).AddItem(nil, 0, 1, false)
	d.pages.AddPage("modal", box, true, true)
	d.app.SetFocus(p)
}
func (d *desktop) prompt(title, value string, submit func(string)) {
	form := tview.NewForm()
	confirm := func() { value := form.GetFormItem(1).(*tview.InputField).GetText(); d.closeModal(); submit(value) }
	lines := max(2, min(5, strings.Count(title, "\n")+1))
	form.AddTextView("", title, 0, lines, true, false).AddInputField("", value, 0, nil, nil).AddButton(d.t("confirm"), confirm).AddButton(d.t("back"), d.closeModal)
	heading := title
	if strings.Contains(title, "\n") {
		heading = d.t("confirm")
	}
	form.SetBorder(true).SetTitle(" " + heading + " ")
	form.SetCancelFunc(d.closeModal)
	form.SetFocus(1)
	form.GetFormItem(1).(*tview.InputField).SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyEnter {
			confirm()
			return nil
		}
		return e
	})
	d.popup(form, 76, 10+lines)
}
func (d *desktop) recordActions() []recordAction {
	var actions []recordAction
	keys := map[string]rune{"rename": 'r', "transcribe": 't', "folder": 'f', "trash": 'x', "restore": 'u', "download": 'd', "device_delete": 'D', "generate_summary": 'i', "retry_summary": 'i'}
	add := func(key string, fn func()) { actions = append(actions, recordAction{key, keys[key], fn}) }
	r, ok := d.record()
	if ok {
		add("rename", func() {
			d.prompt(d.t("rename"), r.Title, func(value string) {
				if strings.TrimSpace(value) != "" {
					d.message(d.s.Update(r.ID, func(r *Record) { r.Title = value; r.TitleSource = "local" }))
					d.refresh()
				}
			})
		})
		if r.Audio != "" && !r.Trashed {
			add("transcribe", func() {
				d.message(d.s.Update(r.ID, func(r *Record) {
					if r.State != "transcribing" {
						r.State = "queued"
						r.Error = ""
					}
				}))
			})
			add("folder", func() {
				command := "open"
				if runtime.GOOS == "linux" {
					command = "xdg-open"
				}
				d.async(func() error { return exec.CommandContext(d.ctx, command, d.s.Dir(r)).Run() })
			})
			add("trash", func() { d.confirmTrash(r) })
		} else if r.Trashed {
			add("restore", func() { d.message(d.s.Update(r.ID, func(r *Record) { r.Trashed = false })); d.refresh() })
		} else {
			add("download", func() { d.message(d.s.Queue("download", r.ID)) })
		}
		if !r.Trashed && r.State == "done" && strings.TrimSpace(r.Transcript) != "" {
			label := "generate_summary"
			if r.SummaryState == "error" {
				label = "retry_summary"
			}
			add(label, func() { d.queueSummary(r.ID) })
		}
		if r.OnDevice {
			add("device_delete", func() { d.confirmDeviceDelete(r) })
		}
	}
	return actions
}
func (d *desktop) async(fn func() error) {
	d.setNotice(d.t("busy"))
	go func() { e := fn(); d.app.QueueUpdateDraw(func() { d.message(e); d.refresh() }) }()
}
func (d *desktop) login() {
	form := tview.NewForm()
	form.AddInputField(d.t("email"), "", 40, nil, nil).AddPasswordField(d.t("password"), "", 40, '•', nil).AddButton(d.t("login"), func() {
		email := form.GetFormItem(0).(*tview.InputField).GetText()
		password := form.GetFormItem(1).(*tview.InputField).GetText()
		form.GetFormItem(1).(*tview.InputField).SetText("")
		d.closeModal()
		d.async(func() error {
			e := d.s.Login(d.ctx, email, password)
			password = ""
			if e != nil {
				return e
			}
			if c := d.s.Config(); c.AutoTranscribe && c.Provider == "doway" {
				_, e = d.s.SyncCloud(d.ctx)
			}
			return e
		})
	}).AddButton(d.t("back"), d.closeModal)
	form.SetBorder(true).SetTitle(" DOWAY ")
	form.SetCancelFunc(d.closeModal)
	d.popup(form, 76, 12)
}
func (d *desktop) generalSettings() {
	c := d.s.Config()
	original := c
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" " + d.t("settings") + " ")
	index := func(value string, values []string) int {
		for i, v := range values {
			if v == value {
				return i
			}
		}
		return 0
	}
	locales := []string{"zh-CN", "en", "ja"}
	providers := []string{"doway", "elevenlabs", "codex", "api", "offline"}
	languages := []string{"", "zh", "en", "ja", "ko", "fr", "es", "ru", "de", "it", "vi", "ar"}
	form.AddDropDown(d.t("locale"), []string{"简体中文", "English", "日本語"}, index(c.Locale, locales), func(_ string, i int) { c.Locale = locales[i] })
	form.AddDropDown(d.t("provider"), []string{"DOWAY · " + d.t("doway_account_short"), "ElevenLabs · Scribe / speakers", "Codex Dictate · Text", "API · timestamps / speakers", "Offline · whisper.cpp"}, index(c.Provider, providers), func(_ string, i int) {
		if c.Provider != providers[i] && providers[i] == "elevenlabs" {
			c.APIURL, c.APIKeyEnv, c.Model = "", "", ""
		}
		c.Provider = providers[i]
	})
	languageLabels := []string{"Auto", "中文", "English", "日本語", "한국어", "Français", "Español", "Русский", "Deutsch", "Italiano", "Tiếng Việt", "العربية"}
	languageIndex := index(c.Language, languages)
	if languageIndex == 0 && c.Language != "" {
		languages = append(languages, c.Language)
		languageLabels = append(languageLabels, c.Language)
		languageIndex = len(languages) - 1
	}
	language := tview.NewDropDown().SetLabel(d.t("spoken")).SetOptions(languageLabels, nil).SetCurrentOption(languageIndex)
	language.SetSelectedFunc(func(_ string, i int) {
		if i >= 0 {
			c.Language = languages[i]
		}
	})
	form.AddFormItem(language)
	form.AddFormItem(newOptionCheckbox(d.t("auto_download"), c.Auto, func(v bool) { c.Auto = v }))
	form.AddFormItem(newOptionCheckbox(d.t("auto_transcribe"), c.AutoTranscribe, func(v bool) { c.AutoTranscribe = v }))
	concurrency := strconv.Itoa(EffectiveTranscriptionConcurrency(c))
	form.AddInputField(d.t("transcription_concurrency"), concurrency, 4, nil, func(v string) { concurrency = v })
	form.AddButton(d.t("save"), func() {
		n, err := strconv.Atoi(concurrency)
		if err != nil || n < 1 || n > 16 {
			d.setNotice(d.t("transcription_concurrency_invalid"))
			form.SetFocus(5)
			d.app.SetFocus(form)
			return
		}
		c.TranscriptionConcurrency = n
		if err := d.s.saveGeneralSettings(original, c); err != nil {
			d.message(err)
			return
		}
		d.closeModal()
		d.makeActions()

		d.refresh()
	})
	form.AddButton(d.t("back"), d.closeModal)
	form.SetCancelFunc(d.closeModal)
	d.popup(form, 76, 19)
}
func (d *desktop) providerSettings() {
	c := effectiveTranscriptionConfig(d.s.Config())
	if c.Provider == "codex" {
		d.providerStatus()
		return
	}
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(d.t("provider"))
	if c.Provider == "doway" {
		form.AddTextView("DOWAY", d.t("doway_provider_help"), 64, 5, false, false)
		form.AddTextView("", d.t("doway_public_upload_help"), 64, 3, false, false)
		form.AddFormItem(newOptionCheckbox(d.t("doway_public_upload"), c.DOWAYPublicUpload, func(v bool) { c.DOWAYPublicUpload = v }))
		form.AddButton(d.t("save"), func() {
			if err := d.s.SaveConfig(c); err != nil {
				d.message(err)
				return
			}
			d.closeModal()
		})
		form.AddButton(d.t("account"), func() { d.closeModal(); d.accountMenu() })
		form.AddButton(d.t("back"), d.closeModal)
		form.SetCancelFunc(d.closeModal)
		d.popup(form, 88, 20)
		return
	}
	if c.Provider == "elevenlabs" {
		form.AddTextView("ElevenLabs", d.t("elevenlabs_provider_help"), 60, 4, false, false)
		form.AddInputField(d.t("api_key_env"), c.APIKeyEnv, 30, nil, func(v string) { c.APIKeyEnv = v })
		form.AddInputField(d.t("api_model"), c.Model, 40, nil, func(v string) { c.Model = v })
	} else if c.Provider == "api" {
		form.AddInputField(d.t("api_url"), c.APIURL, 50, nil, func(v string) { c.APIURL = v }).AddInputField(d.t("api_key_env"), c.APIKeyEnv, 30, nil, func(v string) { c.APIKeyEnv = v }).AddInputField(d.t("api_model"), c.Model, 40, nil, func(v string) { c.Model = v })
		form.AddDropDown(d.t("capabilities"), []string{"whisper-1 · timestamps", "gpt-4o-transcribe-diarize · speakers", "Custom model"}, 2, func(_ string, i int) {
			if i < 2 {
				c.Model = []string{"whisper-1", "gpt-4o-transcribe-diarize"}[i]
				form.GetFormItem(2).(*tview.InputField).SetText(c.Model)
			}
		})
	} else {
		form.AddInputField(d.t("offline_model"), c.OfflineModel, 50, nil, func(v string) { c.OfflineModel = v })
	}
	form.AddButton(d.t("save"), func() {
		if err := d.s.SaveConfig(c); err != nil {
			d.message(err)
			return
		}
		d.closeModal()
	}).AddButton(d.t("back"), d.closeModal)
	form.SetCancelFunc(d.closeModal)
	d.popup(form, 88, 16)
}

type timeline struct {
	*tview.Box
	desktop  *desktop
	dragging bool
}

func clockTime(seconds float64) string {
	if seconds >= 3600 {
		return fmt.Sprintf("%02d:%02d:%02d", int(seconds)/3600, int(seconds)/60%60, int(seconds)%60)
	}
	return fmt.Sprintf("%02d:%02d", int(seconds)/60, int(seconds)%60)
}
