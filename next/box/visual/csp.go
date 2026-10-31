package visual

// CSP returns the sandbox policy for a visual.
func CSP(net bool) string {
	src, script := "data: blob:", "'unsafe-inline'"
	if net {
		src, script = "https: data: blob:", "'unsafe-inline' https:"
	}
	return "default-src 'none'; script-src " + script + "; connect-src " + src + "; img-src " + src +
		"; font-src " + src + "; media-src " + src + "; style-src 'unsafe-inline' " + src +
		"; frame-src 'none'; child-src 'none'; object-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts"
}
