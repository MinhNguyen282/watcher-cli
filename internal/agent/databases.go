package agent

import "github.com/charmbracelet/crush/internal/dbinspector"

func (c *coordinator) databaseConfigs() []dbinspector.DBConfig {
	configured := c.cfg.Config().Databases
	result := make([]dbinspector.DBConfig, 0, len(configured))
	for _, db := range configured {
		result = append(result, dbinspector.DBConfig{Name: db.Name, Type: db.Type, URL: db.URL})
	}
	return result
}
