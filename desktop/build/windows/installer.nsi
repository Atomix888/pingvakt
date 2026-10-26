; PingVakt – Windows installer (NSIS), English and Swedish
; Build with: makensis -DVERSION=0.1.0 -DEXE=..\..\..\dist\PingVakt.exe -DOUT=..\..\..\dist\PingVakt-0.1.0-setup.exe installer.nsi

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
VIAddVersionKey "FileDescription" "${APPNAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "PingVakt"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\PingVakt.exe"
!define MUI_FINISHPAGE_RUN_TEXT "$(RunText)"
!define MUI_LANGDLL_ALLLANGUAGES

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "license.txt"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "Swedish"
!insertmacro MUI_RESERVEFILE_LANGDLL

LangString RunText ${LANG_ENGLISH} "Start PingVakt"
LangString RunText ${LANG_SWEDISH} "Starta PingVakt"
LangString Need64 ${LANG_ENGLISH} "PingVakt requires 64-bit Windows."
LangString Need64 ${LANG_SWEDISH} "PingVakt kräver 64-bitars Windows."
LangString SecMainName ${LANG_ENGLISH} "PingVakt (required)"
LangString SecMainName ${LANG_SWEDISH} "PingVakt (krävs)"
LangString SecDesktopName ${LANG_ENGLISH} "Desktop shortcut"
LangString SecDesktopName ${LANG_SWEDISH} "Genväg på skrivbordet"
LangString SecAutoName ${LANG_ENGLISH} "Start with Windows"
LangString SecAutoName ${LANG_SWEDISH} "Starta med Windows"
LangString DescMain ${LANG_ENGLISH} "The application itself."
LangString DescMain ${LANG_SWEDISH} "Själva programmet."
LangString DescDesktop ${LANG_ENGLISH} "Put a shortcut on the desktop."
LangString DescDesktop ${LANG_SWEDISH} "Lägg en genväg på skrivbordet."
LangString DescAuto ${LANG_ENGLISH} "Start PingVakt automatically when you sign in."
LangString DescAuto ${LANG_SWEDISH} "Starta PingVakt automatiskt när du loggar in."
LangString UninstName ${LANG_ENGLISH} "Uninstall"
LangString UninstName ${LANG_SWEDISH} "Avinstallera"
LangString NeedWebView ${LANG_ENGLISH} "PingVakt needs the Microsoft Edge WebView2 Runtime, which does not seem to be installed.$\r$\n$\r$\nOpen the download page now?"
LangString NeedWebView ${LANG_SWEDISH} "PingVakt behöver Microsoft Edge WebView2 Runtime, som inte verkar vara installerad.$\r$\n$\r$\nVill du öppna nedladdningssidan nu?"

Function .onInit
  !insertmacro MUI_LANGDLL_DISPLAY
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "$(Need64)"
    Abort
  ${EndIf}
  SetRegView 64
FunctionEnd

Section "$(SecMainName)" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"
  File "/oname=PingVakt.exe" "${EXE}"
  File "license.txt"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortCut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\PingVakt.exe"
  CreateShortCut "$SMPROGRAMS\${APPNAME}\$(UninstName) ${APPNAME}.lnk" "$INSTDIR\Uninstall.exe"

  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "PingVakt"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\PingVakt.exe"
  WriteRegStr HKLM "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1

  ; WebView2 ships with Windows 10/11 since 2021 – check anyway
  ReadRegStr $0 HKLM "${WEBVIEW2KEY}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${EndIf}
  ${If} $0 == ""
    MessageBox MB_YESNO|MB_ICONINFORMATION "$(NeedWebView)" IDNO +2
    ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/#download-section"
  ${EndIf}
SectionEnd

Section "$(SecDesktopName)" SecDesktop
  CreateShortCut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\PingVakt.exe"
SectionEnd

Section /o "$(SecAutoName)" SecAutostart
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPNAME}" '"$INSTDIR\PingVakt.exe"'
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} "$(DescMain)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "$(DescDesktop)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecAutostart} "$(DescAuto)"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Function un.onInit
  !insertmacro MUI_UNGETLANGUAGE
FunctionEnd

Section "Uninstall"
  SetRegView 64
  Delete "$INSTDIR\PingVakt.exe"
  Delete "$INSTDIR\license.txt"
  Delete "$INSTDIR\Uninstall.exe"
  Delete "$INSTDIR\Avinstallera.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Uninstall ${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Avinstallera ${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$DESKTOP\${APPNAME}.lnk"
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPNAME}"
  DeleteRegKey HKLM "${UNINSTKEY}"
  ; Settings in %APPDATA%\PingVakt are intentionally kept.
SectionEnd
