package Jiggle::App::Command;
use v5.36;

use App::Cmd::Setup -command;

use POSIX ();

sub library ($self) { $self->app->library }

# Every message gets the time of day, so a long run's log shows where the
# time went.  Output is unbuffered, so a log being watched is current.
sub logger ($self) {
  STDOUT->autoflush(1);
  return sub ($message) {
    say POSIX::strftime('[%H:%M:%S] ', localtime) . $message;
  };
}

1;
