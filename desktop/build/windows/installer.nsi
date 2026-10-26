; PingVakt – Windows-installationsprogram (NSIS)
; Byggs med: makensis -DVERSION=0.1.0 -DEXE=..\..\..\dist\PingVakt.exe -DOUT=..\..\..\dist\PingVakt-0.1.0-setup.exe installer.nsi

Unicode true
!include "MUI2.nsh"
!include "x64.nsh"

!ifndef VERSION
  !define VERSION "0.1.0"
!endif
!ifndef EXE
  !define EXE "..\..\..\dist\PingVakt.exe"
!endif
!ifndef OUT
  !define OUT "..\..\..\dist\PingVakt-${VERSION}-setup.exe"
!endif

!define APPNAME "PingVakt"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\PingVakt"
!define WEBVIEW2KEY "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Name "${APPNAME} ${VERSION}"
OutFile "${OUT}"
InstallDir "$PROGRAMFILES64\${APPNAME}"
InstallDirRegKey HKLM "${UNINSTKEY}" "InstallLocation"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
BrandingText "${APPNAME} ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "FileDescription" "${APPNAME} installationsprogram"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "PingVakt"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\PingVakt.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Starta PingVakt"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "license.txt"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Swedish"

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "PingVakt kräver 64-bitars Windows."
    Abort
  ${EndIf}
  SetRegView 64
FunctionEnd

Section "PingVakt (krävs)" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"
  File "/oname=PingVakt.exe" "${EXE}"
  File "license.txt"
  WriteUninstaller "$INSTDIR\Avinstallera.exe"

  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortCut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\PingVakt.exe"
  CreateShortCut "$SMPROGRAMS\${APPNAME}\Avinstallera ${APPNAME}.lnk" "$INSTDIR\Avinstallera.exe"

  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "PingVakt"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\PingVakt.exe"
  WriteRegStr HKLM "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Avinstallera.exe"'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1

  ; WebView2 finns i Windows 10/11 sedan 2021 – kontrollera ändå
  ReadRegStr $0 HKLM "${WEBVIEW2KEY}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${EndIf}
  ${If} $0 == ""
    MessageBox MB_YESNO|MB_ICONINFORMATION "PingVakt behöver Microsoft Edge WebView2 Runtime, som inte verkar vara installerad.$\r$\n$\r$\nVill du öppna nedladdningssidan nu?" IDNO +2
    ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/#download-section"
  ${EndIf}
SectionEnd

Section "Genväg på skrivbordet" SecDesktop
  CreateShortCut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\PingVakt.exe"
SectionEnd

Section /o "Starta med Windows" SecAutostart
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPNAME}" '"$INSTDIR\PingVakt.exe"'
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} "Själva programmet."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "Lägg en genväg på skrivbordet."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecAutostart} "Starta PingVakt automatiskt när du loggar in."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  SetRegView 64
  Delete "$INSTDIR\PingVakt.exe"
  Delete "$INSTDIR\license.txt"
  Delete "$INSTDIR\Avinstallera.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Avinstallera ${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$DESKTOP\${APPNAME}.lnk"
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPNAME}"
  DeleteRegKey HKLM "${UNINSTKEY}"
  ; Inställningarna i %APPDATA%\PingVakt lämnas kvar med flit.
SectionEnd
