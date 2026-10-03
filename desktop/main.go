// Revoada Desktop: janela nativa (Wails) para o painel do Revoada da sua
// empresa. A primeira tela pede o endereço do painel; depois a janela abre direto
// nele. Sem bindings de Go: nenhuma página carregada na janela consegue chamar
// código nativo — a única porta é /conectar, que exige o nonce desta execução.
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	app, err := novaAplicacao()
	if err != nil {
		log.Fatal(err)
	}
	barra := menu.NewMenu()
	m := barra.AddSubmenu("Revoada")
	m.AddText("Trocar de painel…", keys.CmdOrCtrl("k"), func(*menu.CallbackData) {
		app.trocar()
		runtime.WindowReloadApp(app.ctx)
	})
	m.AddText("Recarregar", keys.CmdOrCtrl("r"), func(*menu.CallbackData) { runtime.WindowReload(app.ctx) })
	m.AddSeparator()
	m.AddText("Sair", keys.CmdOrCtrl("q"), func(*menu.CallbackData) { runtime.Quit(app.ctx) })

	err = wails.Run(&options.App{
		Title: "Revoada", Width: 1360, Height: 860, MinWidth: 900, MinHeight: 600,
		BackgroundColour: &options.RGBA{R: 10, G: 22, B: 38, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets, Handler: app},
		Menu:             barra,
		OnStartup:        app.inicio,
	})
	if err != nil {
		log.Fatal(err)
	}
}
