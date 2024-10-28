package segments

import (
	"errors"
	"fmt"

	"github.com/jandedobbeleer/oh-my-posh/src/properties"
	"github.com/jandedobbeleer/oh-my-posh/src/regex"
	"github.com/jandedobbeleer/oh-my-posh/src/runtime"
	"github.com/jandedobbeleer/oh-my-posh/src/template"
)

const (
	languageTemplate = " {{ if .Error }}{{ .Error }}{{ else }}{{ .Full }}{{ end }} "
	noVersion        = "NO VERSION"
)

type loadContext func()

type inContext func() bool

type getVersion func() (string, error)
type matchesVersionFile func() (string, bool)

type version struct {
	Full          string
	Major         string
	Minor         string
	Patch         string
	Prerelease    string
	BuildMetadata string
	URL           string
	Executable    string
	Expected      string
}

type cmd struct {
	getVersion         getVersion
	executable         string
	regex              string
	versionURLTemplate string
	args               []string
}

func (c *cmd) parse(versionInfo string) (*version, error) {
	values := regex.FindNamedRegexMatch(c.regex, versionInfo)
	if len(values) == 0 {
		return nil, errors.New("cannot parse version string")
	}

	version := &version{
		Full:          values["version"],
		Major:         values["major"],
		Minor:         values["minor"],
		Patch:         values["patch"],
		Prerelease:    values["prerelease"],
		BuildMetadata: values["buildmetadata"],
	}
	return version, nil
}

type language struct {
	base

	projectRoot        *runtime.FileInfo
	loadContext        loadContext
	inContext          inContext
	matchesVersionFile matchesVersionFile
	version
	displayMode        string
	Error              string
	versionURLTemplate string
	commands           []*cmd
	projectFiles       []string
	folders            []string
	extensions         []string
	exitCode           int
	homeEnabled        bool
	Mismatch           bool
}

const (
	// DisplayMode sets the display mode (always, when_in_context, never)
	DisplayMode properties.Property = "display_mode"
	// DisplayModeAlways displays the segment always
	DisplayModeAlways string = "always"
	// DisplayModeFiles displays the segment when the current folder contains certain extensions
	DisplayModeFiles string = "files"
	// DisplayModeEnvironment displays the segment when the environment has a language's context
	DisplayModeEnvironment string = "environment"
	// DisplayModeContext displays the segment when the environment or files is active
	DisplayModeContext string = "context"
	// MissingCommandText sets the text to display when the command is not present in the system
	MissingCommandText properties.Property = "missing_command_text"
	// HomeEnabled displays the segment in the HOME folder or not
	HomeEnabled properties.Property = "home_enabled"
	// LanguageExtensions the list of extensions to validate
	LanguageExtensions properties.Property = "extensions"
	// LanguageFolders the list of folders to validate
	LanguageFolders properties.Property = "folders"
)

func (l *language) Enabled() bool {
	// override default extensions if needed
	l.extensions = l.props.GetStringArray(LanguageExtensions, l.extensions)
	l.folders = l.props.GetStringArray(LanguageFolders, l.folders)
	inHomeDir := func() bool {
		return l.env.Pwd() == l.env.Home()
	}

	var enabled bool

	homeEnabled := l.props.GetBool(HomeEnabled, l.homeEnabled)
	if inHomeDir() && !homeEnabled {
		return false
	}

	if len(l.projectFiles) != 0 && l.hasProjectFiles() {
		enabled = true
	}

	if !enabled {
		// set default mode when not set
		if len(l.displayMode) == 0 {
			l.displayMode = l.props.GetString(DisplayMode, DisplayModeFiles)
		}

		l.loadLanguageContext()

		switch l.displayMode {
		case DisplayModeAlways:
			enabled = true
		case DisplayModeEnvironment:
			enabled = l.inLanguageContext()
		case DisplayModeFiles:
			enabled = l.hasLanguageFiles() || l.hasLanguageFolders()
		case DisplayModeContext:
			fallthrough
		default:
			enabled = l.hasLanguageFiles() || l.hasLanguageFolders() || l.inLanguageContext() || l.hasProjectFiles()
		}
	}

	if !enabled || !l.props.GetBool(properties.FetchVersion, true) {
		return enabled
	}

	err := l.setVersion()
	if err != nil {
		l.Error = err.Error()
	}

	if l.matchesVersionFile != nil {
		expected, match := l.matchesVersionFile()
		if !match {
			l.Mismatch = true
			l.Expected = expected
		}
	}

	return enabled
}

func (l *language) hasLanguageFiles() bool {
	for _, extension := range l.extensions {
		if l.env.HasFiles(extension) {
			return true
		}
	}
	return false
}

func (l *language) hasProjectFiles() bool {
	for _, extension := range l.projectFiles {
		if configPath, err := l.env.HasParentFilePath(extension, false); err == nil {
			l.projectRoot = configPath
			return true
		}
	}

	return false
}

func (l *language) hasLanguageFolders() bool {
	for _, folder := range l.folders {
		if l.env.HasFolder(folder) {
			return true
		}
	}
	return false
}

// setVersion parses the version string returned by the command
func (l *language) setVersion() error {
	var lastError error

	for _, command := range l.commands {
		var versionStr string
		var err error

		if command.getVersion == nil {
			if !l.env.HasCommand(command.executable) {
				lastError = errors.New(noVersion)
				continue
			}

			versionStr, err = l.env.RunCommand(command.executable, command.args...)
			if exitErr, ok := err.(*runtime.CommandError); ok {
				l.exitCode = exitErr.ExitCode
				lastError = fmt.Errorf("err executing %s with %s", command.executable, command.args)
				continue
			}
		} else {
			versionStr, err = command.getVersion()
			if err != nil || versionStr == "" {
				lastError = errors.New("cannot get version")
				continue
			}
		}

		version, err := command.parse(versionStr)
		if err != nil {
			lastError = fmt.Errorf("err parsing info from %s with %s", command.executable, versionStr)
			continue
		}

		l.version = *version
		if command.versionURLTemplate != "" {
			l.versionURLTemplate = command.versionURLTemplate
		}

		l.buildVersionURL()
		l.version.Executable = command.executable

		return nil
	}
	if lastError != nil {
		return lastError
	}
	return errors.New(l.props.GetString(MissingCommandText, ""))
}

func (l *language) loadLanguageContext() {
	if l.loadContext == nil {
		return
	}
	l.loadContext()
}

func (l *language) inLanguageContext() bool {
	if l.inContext == nil {
		return false
	}
	return l.inContext()
}

func (l *language) buildVersionURL() {
	versionURLTemplate := l.props.GetString(properties.VersionURLTemplate, l.versionURLTemplate)
	if len(versionURLTemplate) == 0 {
		return
	}

	tmpl := &template.Text{
		Template: versionURLTemplate,
		Context:  l.version,
	}

	url, err := tmpl.Render()
	if err != nil {
		return
	}

	l.version.URL = url
}
