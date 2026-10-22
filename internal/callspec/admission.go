package callspec

// AccountAdmission names the prepared account and execution of one native
// process. The engine supplies the identity, repository and admission ID.
type AccountAdmission struct {
	ExecutionRef
	Profile   string `json:"profile"`
	Harness   string `json:"harness"`
	AccountID string `json:"accountId"`
	WorkDir   string `json:"workDir"`
}
