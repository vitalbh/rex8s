# fdiff: what this working tree would change on the cluster
Output docs/fdiff.gif
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
Type "fdiff"
Enter
Sleep 5s
