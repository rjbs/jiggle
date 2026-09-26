package Jiggle::App;
use v5.36;

use App::Cmd::Setup -app;

use Jiggle::Library;

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
  $self->{library} //= do {
    my $root = $self->global_options->{library}
            // $ENV{JIGGLE_LIBRARY}
            // '.';

    die "$root doesn't look like a jiggle library (no jiggle.toml)\n"
      unless -e "$root/jiggle.toml";

    Jiggle::Library->new({ root => $root });
  };
}

1;
