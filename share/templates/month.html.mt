% my $label = sub { my ($m) = @_; $site->month_name($m->{month}) . " $m->{year}" };
<%= $site->partial('_period_nav', {
  newer => $newer && { url => $site->month_url($newer->{year}, $newer->{month}), label => $label->($newer) },
  older => $older && { url => $site->month_url($older->{year}, $older->{month}), label => $label->($older) },
}) %>
% my $n = $month->{photos}->@*;
<h1><%= $site->month_name($month->{month}) %> <a href="<%= $site->year_url($month->{year}) %>"><%= $month->{year} %></a>
  <span class="count"><%= $n %> photo<%= $n == 1 ? '' : 's' %></span></h1>
<%= $site->partial('_grid', { photos => $month->{photos} }) %>
