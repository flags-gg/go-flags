package flag

type Details struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type FeatureFlag struct {
	Enabled bool    `json:"enabled"`
	Details Details `json:"details"`
}
