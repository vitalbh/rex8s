# fwatch: follow a commit until Flux applied it and the Deployment rolled out
Output docs/fwatch.gif
Set Shell "bash"
Set FontSize 15
Set Width 1300
Set Height 640
Set Padding 18
Set Theme "Catppuccin Mocha"
Set TypingSpeed 70ms
Hide
Type "export PS1='demo $ ' && clear"
Enter
Sleep 300ms
Show
Hide
Type `cd "$DEMO_FLEET" && clear`
Enter
Show
Type "git --no-pager log --oneline -2"
Enter
Sleep 1s
Type "fwatch -A --interval 1s"
Enter
Sleep 28s
