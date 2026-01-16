if [ -n "$SSH_KEY_PATH" ]; then
  SSH_KEY_ARG="-i $SSH_KEY_PATH"
else
  SSH_KEY_ARG=""
fi

scp $SSH_KEY_ARG -P 18227 launch.py genesis-overrides.json gm@xj7-5.s.filfox.io:~/
scp $SSH_KEY_ARG -P 18228 launch.py join-1.sh gm@xj7-5.s.filfox.io:~/
scp $SSH_KEY_ARG -P 18229 launch.py join-2.sh gm@xj7-5.s.filfox.io:~/
