; The Telegram MCP installer for Windows: per user, without asking for an
; administrator, always into the same folder, because Claude Desktop's
; configuration and "open at login" point at the programs there.
;
;   makensis -DVERSION=1.2.3 -DSRC=<folder with the programs> -DOUT=<setup.exe> installer.nsi
;
; /S installs silently. /UPDATE=<pid> is how the app updates itself: wait for
; that process to end, install, and open the app again.

Unicode true
ManifestDPIAware true
RequestExecutionLevel user
SetCompressor /SOLID lzma

!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

!define PRODUCT "Telegram MCP"
!define EXE "telegram-mcp.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\TelegramMCP"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define WEBVIEW2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Name "${PRODUCT}"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\telegram-mcp"
BrandingText "${PRODUCT} ${VERSION}"

VIProductVersion "${VERSION4}"
VIFileVersion "${VERSION4}"
VIAddVersionKey "ProductName" "${PRODUCT}"
VIAddVersionKey "CompanyName" "Bruno Orlandi"
VIAddVersionKey "FileDescription" "${PRODUCT} Setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "© Bruno Orlandi. MIT License."

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "$(OpenApp)"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
; The installer speaks the language of Windows, as the app does: Portuguese,
; Spanish, or English for any other. The first one listed is the fallback.
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "PortugueseBR"
!insertmacro MUI_LANGUAGE "Spanish"
!insertmacro MUI_LANGUAGE "SpanishInternational"

LangString OpenApp ${LANG_ENGLISH} "Open ${PRODUCT}"
LangString OpenApp ${LANG_PORTUGUESEBR} "Abrir o ${PRODUCT}"
LangString OpenApp ${LANG_SPANISH} "Abrir ${PRODUCT}"
LangString OpenApp ${LANG_SPANISHINTERNATIONAL} "Abrir ${PRODUCT}"
LangString InstallingWebView ${LANG_ENGLISH} "Installing WebView2…"
LangString InstallingWebView ${LANG_PORTUGUESEBR} "Instalando o WebView2…"
LangString InstallingWebView ${LANG_SPANISH} "Instalando WebView2…"
LangString InstallingWebView ${LANG_SPANISHINTERNATIONAL} "Instalando WebView2…"

Var UpdatePID

Function .onInit
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/UPDATE=" $UpdatePID
  ${If} ${Errors}
    StrCpy $UpdatePID ""
  ${EndIf}
FunctionEnd

; waitFor waits up to a minute for the app that asked for the update to quit,
; which it does on its own after starting this installer.
Function waitFor
  ${If} $UpdatePID != ""
    System::Call 'kernel32::OpenProcess(i 0x00100000, i 0, i $UpdatePID) p .r1'
    ${If} $1 P<> 0
      System::Call 'kernel32::WaitForSingleObject(p r1, i 60000) i .r2'
      System::Call 'kernel32::CloseHandle(p r1)'
    ${EndIf}
  ${EndIf}
FunctionEnd

Section "Install"
  Call waitFor
  ; An app left open would keep its programs locked.
  nsExec::Exec 'taskkill /F /IM "${EXE}"'
  Sleep 800

  ; The window draws with WebView2, which Windows 11 and an up-to-date
  ; Windows 10 already have.
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ReadRegStr $1 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${If} $0 == ""
  ${AndIf} $1 == ""
    DetailPrint "$(InstallingWebView)"
    SetOutPath "$PLUGINSDIR"
    File "MicrosoftEdgeWebview2Setup.exe"
    ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install'
  ${EndIf}

  SetOutPath "$INSTDIR"
  ; Claude Desktop keeps the bridge running for its whole session, and a
  ; running program cannot be overwritten, but it can be renamed: the new one
  ; takes its place, and the old file goes once nothing runs it.
  Delete "$INSTDIR\telegram-mcp-bridge.exe.old"
  ${If} ${FileExists} "$INSTDIR\telegram-mcp-bridge.exe"
    Rename "$INSTDIR\telegram-mcp-bridge.exe" "$INSTDIR\telegram-mcp-bridge.exe.old"
  ${EndIf}
  ; The app itself gets the same treatment, with retries. The app that asked
  ; for the update has exited, but Windows or a security scanner can keep
  ; its program open for a moment after that. A plain File then fails, and a
  ; silent install skips it: the old program stays while the registry names
  ; the new version, and the update can neither finish nor be recognised as
  ; never activated. So rename the old program aside (allowed even while it
  ; is mapped), retry for up to 30 seconds, and fail loudly otherwise.
  StrCpy $R1 0
  replace_exe:
    Delete "$INSTDIR\${EXE}.old"
    ClearErrors
    ${If} ${FileExists} "$INSTDIR\${EXE}"
      Rename "$INSTDIR\${EXE}" "$INSTDIR\${EXE}.old"
    ${EndIf}
    ${IfNot} ${Errors}
      SetOverwrite try
      File "${SRC}\${EXE}"
      SetOverwrite on
    ${EndIf}
    ${If} ${Errors}
      IntOp $R1 $R1 + 1
      ${If} $R1 < 60
        Sleep 500
        Goto replace_exe
      ${EndIf}
      ${IfNot} ${FileExists} "$INSTDIR\${EXE}"
        Rename "$INSTDIR\${EXE}.old" "$INSTDIR\${EXE}"
      ${EndIf}
      SetErrorLevel 4
      Abort "$INSTDIR\${EXE} could not be replaced"
    ${EndIf}
  Delete /REBOOTOK "$INSTDIR\${EXE}.old"
  File "${SRC}\telegram-mcp-bridge.exe"
  Delete /REBOOTOK "$INSTDIR\telegram-mcp-bridge.exe.old"
  File "${SRC}\THIRD-PARTY-NOTICES.txt"

  CreateShortcut "$SMPROGRAMS\${PRODUCT}.lnk" "$INSTDIR\${EXE}"

  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "Bruno Orlandi"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "https://telegram-mcp.bwoliveira.xyz"
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" $0

  ${If} $UpdatePID != ""
    Exec '"$INSTDIR\${EXE}"'
  ${EndIf}
SectionEnd

; Uninstalling removes the programs, not what the app keeps in
; %LOCALAPPDATA%\telegram-mcp: the messages, the Telegram session and the
; transcription model stay until the app's "Apagar todos os dados" or a
; manual delete.
Section "Uninstall"
  nsExec::Exec 'taskkill /F /IM "${EXE}"'
  Sleep 800
  Delete "$INSTDIR\${EXE}"
  Delete /REBOOTOK "$INSTDIR\${EXE}.old"
  Delete /REBOOTOK "$INSTDIR\telegram-mcp-bridge.exe"
  Delete /REBOOTOK "$INSTDIR\telegram-mcp-bridge.exe.old"
  Delete "$INSTDIR\THIRD-PARTY-NOTICES.txt"

  Delete "$INSTDIR\uninstall.exe"
  RMDir /REBOOTOK "$INSTDIR"
  Delete "$SMPROGRAMS\${PRODUCT}.lnk"
  DeleteRegValue HKCU "${RUN_KEY}" "com.bwoliveira.telegram-mcp"
  RMDir /r "$APPDATA\${EXE}"
  DeleteRegKey HKCU "${UNINST_KEY}"
SectionEnd
