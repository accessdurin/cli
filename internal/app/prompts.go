package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/accessdurin/cli/internal/clierror"
)

// This file stands in for @clack/prompts, which src/terminal-ui.ts uses, and holds guard
// (src/prompts.ts). Questions are huh forms: arrow keys and Enter choose an option, typing
// and Enter answer a text question, Ctrl-C cancels. Messages are printed in clack's layout: a
// gutter of │ with a symbol before each block.

// errSetupCancelled is what every question fails with once the user cancels it.
var errSetupCancelled = clierror.New("Setup cancelled before completion. Run durin to resume.")

// promptOption is one choice of a select question. Hint, when set, is shown beside its label.
type promptOption struct {
	Value, Label, Hint string
}

// prompter is the interactive terminal: questions read from in and draw on out; messages
// are written to out too, with colours reduced to what it supports (none when it is not a
// terminal).
type prompter struct {
	in       io.Reader
	out      io.Writer
	messages io.Writer
	style    promptStyles
	terminal *promptInput // shared reader of in, once in is known to be a terminal
}

type promptStyles struct {
	bar, step, info, active, cancel, dim lipgloss.Style
}

// newPrompter uses huh v2 (Bubble Tea v2). Version 1 queried the terminal's background
// colour when the package was initialized, in every durin command: in a terminal that does
// not answer, that stalled startup for seconds and swallowed the keys typed meanwhile.
// Colours here are ANSI palette indices, so they suit light and dark terminals alike.
func newPrompter(in io.Reader, out io.Writer) *prompter {
	color := func(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }
	return &prompter{in: in, out: out, messages: colorprofile.NewWriter(out, os.Environ()), style: promptStyles{
		bar: color("8"), step: color("2"), info: color("4"), active: color("6"), cancel: color("1"), dim: color("8"),
	}}
}

func (p *prompter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.messages, format, args...)
}

// block prints a gutter line, then symbol and the first line of message, then the rest of
// its lines in the gutter.
func (p *prompter) block(symbol, message string) {
	p.printf("%s\n", p.style.bar.Render("│"))
	p.symbolLines(symbol, message)
}

func (p *prompter) symbolLines(symbol, message string) {
	lines := strings.Split(message, "\n")
	p.printf("%s  %s\n", symbol, lines[0])
	for _, line := range lines[1:] {
		p.printf("%s  %s\n", p.style.bar.Render("│"), line)
	}
}

// intro is p.intro.
func (p *prompter) intro(title string) { p.printf("%s  %s\n", p.style.bar.Render("┌"), title) }

// outro is p.outro.
func (p *prompter) outro(message string) {
	p.printf("%s\n%s  %s\n\n", p.style.bar.Render("│"), p.style.bar.Render("└"), message)
}

// step is p.log.step.
func (p *prompter) step(message string) { p.block(p.style.step.Render("◇"), message) }

// info is p.log.info.
func (p *prompter) info(message string) { p.block(p.style.info.Render("●"), message) }

// note is p.note: a titled block.
func (p *prompter) note(message, title string) {
	lines := strings.Split(message, "\n")
	total := 3 + lipgloss.Width(title) + 4
	for _, line := range lines {
		total = max(total, 3+lipgloss.Width(line)+2)
	}
	bar := p.style.bar.Render("│")
	rule := strings.Repeat("─", total-3-lipgloss.Width(title)-1)
	p.printf("%s\n%s  %s %s\n", bar, p.style.step.Render("◇"), title, p.style.bar.Render(rule))
	for _, line := range lines {
		p.printf("%s  %s\n", bar, p.style.dim.Render(line))
	}
	p.printf("%s\n", p.style.bar.Render("├"+strings.Repeat("─", total-1)))
}

// cancel is p.cancel.
func (p *prompter) cancel(message string) {
	p.printf("%s  %s\n\n", p.style.bar.Render("└"), p.style.cancel.Render(message))
}

// guard is src/prompts.ts guard: a cancelled question prints "Cancelled." and stops setup
// with an error that says how to resume. Other errors pass through.
func (p *prompter) guard(question string, err error) error {
	if !errors.Is(err, huh.ErrUserAborted) {
		return err
	}
	p.symbolLines(p.style.cancel.Render("■"), p.style.dim.Render(question)+"\n")
	p.cancel("Cancelled.")
	return errSetupCancelled
}

// answered leaves the question and its answer on screen where the form was, as clack does
// once it is submitted.
func (p *prompter) answered(question, answer string) {
	p.symbolLines(p.style.step.Render("◇"), question+"\n"+p.style.dim.Render(answer))
}

func (p *prompter) theme() huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		styles := huh.ThemeBase16(isDark)
		styles.Focused.Title = styles.Focused.Title.Bold(true)
		return styles
	})
}

func (p *prompter) run(ctx context.Context, field huh.Field) error {
	p.printf("%s\n", p.style.bar.Render("│"))
	input, done := p.promptReader()
	defer done()
	return huh.NewForm(huh.NewGroup(field)).
		WithTheme(p.theme()).
		WithShowHelp(false).
		WithInput(input).
		WithOutput(p.out).
		RunWithContext(ctx)
}

// selectOne is p.select: the chosen option's value.
func (p *prompter) selectOne(ctx context.Context, message string, options []promptOption) (string, error) {
	choices := make([]huh.Option[string], len(options))
	labels := map[string]string{}
	for i, option := range options {
		key := option.Label
		if option.Hint != "" {
			key += " (" + option.Hint + ")"
		}
		choices[i] = huh.NewOption(key, option.Value)
		if _, seen := labels[option.Value]; !seen {
			labels[option.Value] = option.Label
		}
	}
	var value string
	field := huh.NewSelect[string]().Title(message).Options(choices...).Value(&value)
	if err := p.run(ctx, field); err != nil {
		return "", p.guard(message, err)
	}
	p.answered(message, labels[value])
	return value, nil
}

// text is p.text: what the user typed, once validate (which returns an error message, or ""
// to accept) accepts it.
func (p *prompter) text(ctx context.Context, message string, validate func(string) string) (string, error) {
	var value string
	field := huh.NewInput().Title(message).Value(&value).Validate(func(s string) error {
		if problem := validate(s); problem != "" {
			return errors.New(problem)
		}
		return nil
	})
	if err := p.run(ctx, field); err != nil {
		return "", p.guard(message, err)
	}
	p.answered(message, TerminalText(value))
	return value, nil
}
