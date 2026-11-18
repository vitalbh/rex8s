# kview: the clusters like k9s — tabs, live lists (a changed row lights up),
# jobs grouped by CronJob, ":" for any kind, a key per action
Output docs/kview.gif
Set Shell "bash"
Set FontSize 15
Set Width 1300
Set Height 680
Set Padding 18
Set Theme "Catppuccin Mocha"
Env DEMO_CRASH "1"
Set TypingSpeed 80ms
Hide
Type "export PS1='demo $ ' && clear"
Enter
Sleep 300ms
Show
Type "kview -A"
Enter
Sleep 4s
Down
Sleep 400ms
Down
Sleep 400ms
Down
Sleep 1s
Type "/"
Sleep 300ms
Type "worker"
Sleep 1200ms
Enter
Sleep 1s
Type "R"
Sleep 2s
Down 4
Sleep 1s
Type "d"
Sleep 3s
Down 6
Sleep 1500ms
Type "q"
Sleep 1s
Escape
Sleep 800ms
Tab
Sleep 3s
Down
Sleep 400ms
Down
Sleep 1s
Enter
Sleep 3s
Type "4"
Sleep 2500ms
Enter
Sleep 3s
Escape
Sleep 1s
Type ":"
Sleep 300ms
Type "c"
Sleep 1500ms
Down
Sleep 400ms
Down
Sleep 400ms
Up
Sleep 400ms
Up
Sleep 600ms
Enter
Sleep 3s
Escape
Sleep 800ms
Type "6"
Sleep 3500ms
Type "5"
Sleep 3s
Type "q"
Sleep 500ms
