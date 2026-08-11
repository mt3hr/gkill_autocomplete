package gkillauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/mt3hr/gkill/src/server/gkill/dao/server_config"
)

// ErrNoEnabledDevice は有効なデバイス設定が無いことを表す。
var ErrNoEnabledDevice = errors.New("no enabled device found in gkill server config")

// ServerSettings は gkill 本体のサーバ設定から読み取った値。
type ServerSettings struct {
	// Device は有効になっているデバイス名。セッション発行に使う。
	Device string

	// EnableTLS は gkill が TLS を使っているか。
	EnableTLS bool

	// CertFile と KeyFile は展開済みの絶対パス。
	// DB には "$HOME/gkill/tls/cert.cer" のような未展開の文字列で入っているので、
	// 読んだ側で必ず展開する。
	CertFile string
	KeyFile  string

	// BaseURL は gkill 本体の宛先。設定に書かせずここから決める。
	BaseURL string
}

// LoadServerSettings は gkill のサーバ設定を読む。
//
// 有効なデバイスの選び方は gkill の ResolveLocalServerEndpoint と同じで、
// EnableThisDevice が真の最初の1件を採る。
func LoadServerSettings(ctx context.Context, configDir string, logger *slog.Logger) (ServerSettings, error) {
	serverConfigDBPath := ServerConfigDBPath(configDir)
	if _, err := os.Stat(serverConfigDBPath); err != nil {
		return ServerSettings{}, fmt.Errorf(
			"gkill のサーバ設定が見つかりません(%s)。先に gkill を起動してください: %w",
			serverConfigDBPath, err)
	}

	serverConfigDAO, err := server_config.NewServerConfigDAOSQLite3Impl(ctx, serverConfigDBPath)
	if err != nil {
		return ServerSettings{}, fmt.Errorf("error at open server config dao: %w", err)
	}
	defer func() {
		if err := serverConfigDAO.Close(ctx); err != nil {
			logger.Debug("サーバ設定DBを閉じられませんでした", slog.String("error", err.Error()))
		}
	}()

	serverConfigs, err := serverConfigDAO.GetAllServerConfigs(ctx)
	if err != nil {
		return ServerSettings{}, fmt.Errorf("error at get all server configs: %w", err)
	}

	for _, serverConfig := range serverConfigs {
		if !serverConfig.EnableThisDevice {
			continue
		}

		scheme := "http"
		if serverConfig.EnableTLS {
			scheme = "https"
		}

		return ServerSettings{
			Device:    serverConfig.Device,
			EnableTLS: serverConfig.EnableTLS,
			CertFile:  expandPath(serverConfig.TLSCertFile),
			KeyFile:   expandPath(serverConfig.TLSKeyFile),
			BaseURL:   fmt.Sprintf("%s://localhost%s", scheme, portSuffix(serverConfig.Address)),
		}, nil
	}
	return ServerSettings{}, fmt.Errorf("%w: %s", ErrNoEnabledDevice, serverConfigDBPath)
}

// EnsureTLSFilesExist は証明書と鍵が実在するかを確かめる。
//
// 無いまま ListenAndServeTLS に渡すと、待ち受けに失敗した理由が分かりにくいので、
// 先に何が足りないかを言う。
func (s ServerSettings) EnsureTLSFilesExist() error {
	if _, err := os.Stat(s.CertFile); err != nil {
		return fmt.Errorf("gkill の TLS 証明書が見つかりません(%s)。"+
			"gkill の設定画面で証明書を作り直してください: %w", s.CertFile, err)
	}
	if _, err := os.Stat(s.KeyFile); err != nil {
		return fmt.Errorf("gkill の TLS 秘密鍵が見つかりません(%s)。"+
			"gkill の設定画面で証明書を作り直してください: %w", s.KeyFile, err)
	}
	return nil
}

// expandPath は環境変数を展開して区切りを揃える。
func expandPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(os.ExpandEnv(path))
}

// portSuffix はアドレス文字列からポート部分(":9999")を取り出す。
func portSuffix(address string) string {
	if address == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return ":" + port
}
