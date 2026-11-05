# knode: a shell on a node (kubectl debug node), pod removed on exit
Output docs/knode.gif
Set Shell "bash"
Set FontSize 15
Set Width 1300
Set Height 520
Set Padding 18
Set Theme "Catppuccin Mocha"
Set TypingSpeed 80ms
Hide
Type "export PS1='demo $ ' && clear"
Enter
Sleep 300ms
Show
Type "knode"
Enter
Sleep 3s
Down
Sleep 800ms
Enter
Sleep 1500ms
Type "use"
Enter
Sleep 2s
Type "chroot /host crictl ps"
Sleep 400ms
Enter
Sleep 1500ms
Type "exit"
Enter
Sleep 2500ms
