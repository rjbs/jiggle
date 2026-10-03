package Jiggle::App::Command::edit;
use v5.36;

use Jiggle::App -command;

use Jiggle::Editor;

sub abstract { 'review and edit a batch of photos in the browser' }

sub usage_desc { '%c edit %o QUERY...' }

sub description {
  return <<~'END';
  This picks a batch of photos and serves an editor for them on localhost.
  The batch is every photo matching all the query's terms:

    pending  private  public  all
    album:SLUG  tag:TAG  year:YYYY  id:ID  limit:N

  For example, "jiggle edit pending" reviews newly ingested photos.

  Given exactly one term, album:SLUG, it edits the album too: its title,
  description, cover, and order.

  END
}

sub opt_spec {
  return (
    [ 'port|p=i', 'port to listen on', { default => 3001 } ],
    [ 'no-open',  "don't open the editor in a browser" ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('no query given; try "pending"') unless @$args;
}

sub execute ($self, $opt, $args) {
  Jiggle::Editor->serve_query($self->library, $args, {
    port => $opt->port,
    open => ! $opt->no_open,
  });
}

1;
