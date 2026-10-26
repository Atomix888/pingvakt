// Command winres skapar Windows-resurser (ikon, manifest, versionsinfo) för
// skrivbordsappen: desktop/rsrc_windows_<arch>.syso. Körs före Windows-bygget.
//
//	go run ./tools/winres -version 0.1.0
package main

import (
	"flag"
	"image/png"
	"log"
	"os"
	"strings"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func main() {
	ver := flag.String("version", "0.1.0", "programversion")
	iconPath := flag.String("icon", "desktop/build/appicon.png", "ikon (PNG)")
	outDir := flag.String("out", "desktop", "utmapp")
	flag.Parse()

	f, err := os.Open(*iconPath)
	if err != nil {
		log.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	icon, err := winres.NewIconFromResizedImage(img, nil)
	if err != nil {
		log.Fatal(err)
	}
	rs := &winres.ResourceSet{}
	if err := rs.SetIcon(winres.Name("APP"), icon); err != nil {
		log.Fatal(err)
	}
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "se.pingvakt.desktop"},
		Description:         "PingVakt",
		ExecutionLevel:      winres.AsInvoker,
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
	})
	v := strings.TrimPrefix(*ver, "v")
	vi := version.Info{}
	vi.SetFileVersion(v)
	vi.SetProductVersion(v)
	for k, val := range map[string]string{
		version.ProductName:      "PingVakt",
		version.FileDescription:  "PingVakt – övervakning av latens och paketförlust",
		version.CompanyName:      "PingVakt",
		version.LegalCopyright:   "© PingVakt. Innehåller kod under MIT-licens.",
		version.OriginalFilename: "PingVakt.exe",
		version.ProductVersion:   v,
		version.FileVersion:      v,
	} {
		if err := vi.Set(version.LangDefault, k, val); err != nil {
			log.Fatal(err)
		}
	}
	rs.SetVersionInfo(vi)

	for _, arch := range []winres.Arch{winres.ArchAMD64, winres.ArchARM64} {
		name := *outDir + "/rsrc_windows_" + string(arch) + ".syso"
		out, err := os.Create(name)
		if err != nil {
			log.Fatal(err)
		}
		if err := rs.WriteObject(out, arch); err != nil {
			log.Fatal(err)
		}
		out.Close()
		log.Println("skrev", name)
	}
}
