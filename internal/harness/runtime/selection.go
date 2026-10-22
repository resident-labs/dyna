package runtime

// SelectedVersion reads an explicit selection without scanning or hashing
// bundles. An empty version means Resolve uses the newest installed bundle.
func (m *BundleManager) SelectedVersion(harness string) (string, error) {
	h, err := bundleHarness(harness)
	if err != nil {
		return "", err
	}
	return m.selected(h), nil
}

// LastGoodVersion reads the rollback target without scanning or hashing
// bundles. An empty version means no update has replaced a known-good
// selection.
func (m *BundleManager) LastGoodVersion(harness string) (string, error) {
	h, err := bundleHarness(harness)
	if err != nil {
		return "", err
	}
	return m.lastGood(h), nil
}
