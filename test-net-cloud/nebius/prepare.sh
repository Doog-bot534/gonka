if [ -n "$SSH_KEY_PATH" ]; then
  SSH_KEY_ARG="-i $SSH_KEY_PATH"
else
  SSH_KEY_ARG=""
fi

scp $SSH_KEY_ARG launch.py genesis-overrides.json ubuntu@89.169.110.61:/srv/dai/
scp $SSH_KEY_ARG -p 18227 launch.py join-1.sh decentai@xj7-5.s.filfox.io:/srv/dai/
scp $SSH_KEY_ARG -p 18228 launch.py join-2.sh decentai@xj7-5.s.filfox.io:/srv/dai/
