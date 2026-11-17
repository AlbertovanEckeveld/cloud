package app

import "errors"

// sidebarSupported: macOS offers no supported way for an app to change the
// Finder sidebar; users drag the folder there themselves.
const sidebarSupported = false

func setSidebar(string, string, bool) error {
	return errors.New("de Finder-zijbalk kan niet door een app worden aangepast; sleep de map erin")
}
