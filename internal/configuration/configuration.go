package configuration

import (
	"github.com/spf13/viper"
)

var Config *AppConfig

func init() {
	Config = loadFromEnv()
}

type AppConfig struct {
	AppName        string
	AppVersion     string
	AppRevision    string
	AppBuiltAt     string
	AppDataDir     string
	Env            string
	RestConfig     *RestConfig
	DatabaseConfig *DatabaseConfig
}

type RestConfig struct {
	Host string
	Port int
}

type DatabaseConfig struct {
	DSN      string
	Name     string
	Host     string
	Port     int
	Username string
	Password string
	SSL      string // disable | require | verify-ca | verify-full
	Addr     string
}

func loadFromEnv() *AppConfig {
	viper.AutomaticEnv()

	return &AppConfig{
		AppName:     viper.GetString("APP_NAME"),
		AppVersion:  viper.GetString("APP_VERSION"),
		Env:         viper.GetString("APP_ENV"),
		AppDataDir:  viper.GetString("APP_DATA_DIR"),
		AppRevision: viper.GetString("APP_REVISION"),
		AppBuiltAt:  viper.GetString("APP_BUILT_AT"),
		RestConfig: &RestConfig{
			Host: viper.GetString("APP_REST_HOST"),
			Port: viper.GetInt("APP_REST_PORT"),
		},
		DatabaseConfig: &DatabaseConfig{
			DSN:      viper.GetString("APP_DB__DSN"),
			Name:     viper.GetString("APP_DB__NAME"),
			Host:     viper.GetString("APP_DB__HOST"),
			Port:     viper.GetInt("APP_DB__PORT"),
			Username: viper.GetString("APP_DB__USERNAME"),
			Password: viper.GetString("APP_DB__PASSWORD"),
			SSL:      viper.GetString("APP_DB_SSL"),
			Addr:     viper.GetString("APP_DB_ADDR"),
		},
	}
}
