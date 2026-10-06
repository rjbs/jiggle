package Jiggle::App;
use v5.36;

use App::Cmd::Setup -app;

use Jiggle::Library;
use POSIX ();

=head1 NAME

Jiggle::App - the jiggle command

=head1 DESCRIPTION

Every command works on one library, found from the C<--library> switch, then
the C<JIGGLE_LIBRARY> environment variable, then the current directory.

=cut

sub global_opt_spec {
  return (
    [ 'library|L=s', 'the library to work on' ],
  );
}

sub library ($self) {
  $self->{library} //= Jiggle::Library->new({ root => $self->library_root, logger => $self->logger });
}

sub library_root ($self) {
  my $root = $self->global_options->{library}
          // $ENV{JIGGLE_LIBRARY}
          // '.';

  die "$root doesn't look like a jiggle library (no jiggle.toml)\n"
    unless -e "$root/jiggle.toml";

  return $root;
}

# Every message gets the time of day, so a long run's log shows where the
# time went.  Output is unbuffered, so a log being watched is current.
sub logger ($self) {
  STDOUT->autoflush(1);
  return sub ($message) {
    say POSIX::strftime('[%H:%M:%S] ', localtime) . $message;
  };
}

1;
